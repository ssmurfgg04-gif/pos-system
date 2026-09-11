package services

import (
	"context"
	"log"
	"time"

	"posapp/internal/models"
)

// Sweeper polls pending STK payments every 5s. This is the real-world
// completion path for LAN deployments (no public webhook) — Daraja's
// stkpushquery answers what the customer did on their phone. Payments older
// than 3 minutes time out as FAILED (the order stays PENDING for manual
// fallback or void).
type Sweeper struct {
	svc      *Service
	interval time.Duration
	timeout  time.Duration
}

func NewSweeper(s *Service) *Sweeper {
	return &Sweeper{svc: s, interval: 5 * time.Second, timeout: 3 * time.Minute}
}

func (sw *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(sw.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sw.tick(ctx)
		}
	}
}

func (sw *Sweeper) tick(ctx context.Context) {
	s := sw.svc
	// Collect pending STK payments (rows closed before any provider calls).
	type pending struct {
		id               int64
		checkoutID       string
		createdAt        time.Time
	}
	rows, err := s.db.Query(`SELECT id, checkout_request_id, created_at FROM payments
		WHERE status = 'PENDING' AND checkout_request_id != ''`)
	if err != nil {
		return
	}
	var list []pending
	now := time.Now()
	for rows.Next() {
		var p pending
		var created string
		if err := rows.Scan(&p.id, &p.checkoutID, &created); err != nil {
			rows.Close()
			return
		}
		if t, err := time.Parse("2006-01-02 15:04:05", created); err == nil {
			p.createdAt = t
		} else if t, err := time.Parse(time.RFC3339, created); err == nil {
			p.createdAt = t
		} else {
			p.createdAt = now
		}
		list = append(list, p)
	}
	rows.Close()
	if rows.Err() != nil {
		return
	}

	provider := s.GetProvider()
	for _, p := range list {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if now.Sub(p.createdAt) > sw.timeout {
			s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'STK timeout (3 min)' WHERE id = ? AND status = 'PENDING'`), p.id)
			continue
		}
		res, err := provider.QuerySTK(ctx, p.checkoutID)
		if err != nil {
			continue // transient provider error — keep waiting
		}
		switch {
		case res.ResultCode == 0:
			if _, err := s.completePayment(p.id, res.MpesaReceiptNumber, res.AmountCents, res.ResultDesc); err != nil {
				log.Printf("[sweeper] complete payment %d: %v", p.id, err)
			}
		case res.ResultCode > 0:
			// Definitive failure (cancelled / insufficient funds / ...).
			s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
				truncStr(res.ResultDesc, 200), p.id)
		default:
			// -1 = still in progress
		}
	}
	_ = models.PaymentPending
}
