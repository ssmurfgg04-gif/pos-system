package services

// promotions.go — bulk SMS / WhatsApp promotions to customers (v1.12.0).
//
// Rules that keep a shop on the right side of Kenya's Data Protection Act
// (direct marketing needs consent and an easy way out) and its customers'
// patience:
//   - Only customers marked as agreeing to offers are ever sent to
//     (customers.marketing_opt_in; off for everyone until the shop records
//     it). Opting out on one till reaches every till (sync "consent").
//   - Every message carries the opt-out line (setting promo_footer).
//   - Nothing goes out in quiet hours (promo_quiet_start..promo_quiet_end,
//     Nairobi time, default 20:00-08:00); a campaign waits and resumes.
//   - A campaign is a frozen list of recipients made when it is created;
//     the owner sees the count and the estimated SMS cost before sending.
//   - Each recipient is sent AT MOST once: rows are marked 'sending' before
//     the provider is called; a till that stops mid-send marks those rows
//     failed ("result unknown") instead of sending again.
//
// SMS goes through Africa's Talking (the shop's own account: username,
// API key, optional approved sender ID). WhatsApp goes through the
// gateway already used for receipts, slowly (one message every few
// seconds) so the shop's number is not flagged.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"posapp/internal/auth"
)

const (
	promoTick          = 15 * time.Second
	promoSMSBatch      = 100 // numbers per Africa's Talking call
	promoWhatsappBatch = 4   // WhatsApp messages per tick
	promoWhatsappGap   = 3 * time.Second
	promoMaxAttempts   = 3
	promoMaxRecipients = 20000
	promoStuckAfter    = 10 * time.Minute
)

type PromoSegment struct {
	MinSpentCents     int64 `json:"minSpentCents"`     // lifetime paid sales at least this
	VisitedWithinDays int   `json:"visitedWithinDays"` // bought something in the last N days
	AwayForDays       int   `json:"awayForDays"`       // has bought before, but nothing in N days (win-back)
	MinPoints         int64 `json:"minPoints"`         // loyalty points at least
	WithTabBalance    bool  `json:"withTabBalance"`    // owes on a tab
}

type PromoAudience struct {
	Count       int      `json:"count"`
	Sample      []string `json:"sample"`
	NoConsent   int      `json:"noConsent"` // matching customers who have not agreed to offers
	BadPhone    int      `json:"badPhone"`
	Segments    int      `json:"segments"` // SMS parts per message (longest personalised body)
	Characters  int      `json:"characters"`
	PreviewBody string   `json:"previewBody"`
}

type PromoCampaign struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Channel     string       `json:"channel"`
	Body        string       `json:"body"`
	Segment     PromoSegment `json:"segment"`
	Status      string       `json:"status"` // draft | sending | paused | done | cancelled
	Total       int          `json:"total"`
	Sent        int          `json:"sent"`
	Failed      int          `json:"failed"`
	Pending     int          `json:"pending"`
	LastError   string       `json:"lastError"`
	CreatedBy   string       `json:"createdBy"`
	CreatedAt   string       `json:"createdAt"`
	StartedAt   string       `json:"startedAt"`
	FinishedAt  string       `json:"finishedAt"`
	QuietNow    bool         `json:"quietNow"`
	Segments    int          `json:"segments"`
	ProviderSet bool         `json:"providerSet"`
}

type PromoRecipient struct {
	Name   string `json:"name"`
	Phone  string `json:"phone"`
	Status string `json:"status"`
	Error  string `json:"error"`
	SentAt string `json:"sentAt"`
}

type CreatePromoRequest struct {
	Name    string       `json:"name"`
	Channel string       `json:"channel"`
	Body    string       `json:"body"`
	Segment PromoSegment `json:"segment"`
}

// ---- message text ------------------------------------------------------------

// smsParts counts SMS segments: GSM-7 160 (153 when split), otherwise
// UCS-2 70 (67 when split). Emoji and most non-Latin text force UCS-2.
func smsParts(body string) int {
	n := 0
	gsm := true
	for _, r := range body {
		if !isGSM7(r) {
			gsm = false
		}
		if strings.ContainsRune("^{}\\[~]|€", r) {
			n += 2 // escape + char
		} else {
			n++
		}
	}
	if !gsm {
		n = utf8.RuneCountInString(body)
		if n <= 70 {
			return 1
		}
		return (n + 66) / 67
	}
	if n <= 160 {
		return 1
	}
	return (n + 152) / 153
}

func isGSM7(r rune) bool {
	const basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà^{}\\[~]|€"
	return strings.ContainsRune(basic, r)
}

func firstName(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return "there"
	}
	return f[0]
}

// personalise fills {name} {points} {shop} {phone} and adds the opt-out line.
func (s *Service) personalise(body, name string, points int64) string {
	shop := firstNonEmpty(s.settings.Get("store_name"), s.settings.Get("app_name"), "us")
	phone := s.settings.Get("store_phone")
	r := strings.NewReplacer("{name}", firstName(name), "{points}", fmt.Sprint(points), "{shop}", shop, "{phone}", phone)
	out := strings.TrimSpace(r.Replace(body))
	footer := strings.TrimSpace(s.settings.Get("promo_footer"))
	if footer == "" {
		footer = "To stop offers, tell us or call {phone}."
	}
	footer = r.Replace(footer)
	if phone == "" {
		footer = strings.ReplaceAll(footer, " or call .", ".")
	}
	return out + "\n" + footer
}

// ---- audience ----------------------------------------------------------------

type promoTarget struct {
	id     int64
	name   string
	phone  string
	points int64
}

func (s *Service) promoTargets(seg PromoSegment) (targets []promoTarget, noConsent, badPhone int, err error) {
	q := `SELECT c.id, c.name, COALESCE(c.phone,''), c.loyalty_points, COALESCE(c.marketing_opt_in,0),
		COALESCE((SELECT SUM(o.total_cents) FROM orders o WHERE o.customer_id = c.id AND o.status = 'PAID'),0),
		COALESCE((SELECT MAX(o.created_at) FROM orders o WHERE o.customer_id = c.id AND o.status = 'PAID'),''),
		c.balance_cents
		FROM customers c WHERE c.is_active = 1 ORDER BY c.id`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	now := time.Now()
	seen := map[string]bool{}
	for rows.Next() {
		var t promoTarget
		var optIn int
		var spent, bal any
		var last string
		if err := rows.Scan(&t.id, &t.name, &t.phone, &t.points, &optIn, &spent, &last, &bal); err != nil {
			return nil, 0, 0, err
		}
		if seg.MinSpentCents > 0 && asInt64(spent) < seg.MinSpentCents {
			continue
		}
		if seg.MinPoints > 0 && t.points < seg.MinPoints {
			continue
		}
		if seg.WithTabBalance && asInt64(bal) <= 0 {
			continue
		}
		var lastT time.Time
		if last != "" {
			lastT, _ = parseClock(normTime(last))
		}
		if seg.VisitedWithinDays > 0 && (lastT.IsZero() || now.Sub(lastT) > time.Duration(seg.VisitedWithinDays)*24*time.Hour) {
			continue
		}
		if seg.AwayForDays > 0 && (lastT.IsZero() || now.Sub(lastT) < time.Duration(seg.AwayForDays)*24*time.Hour) {
			continue
		}
		if optIn != 1 {
			noConsent++
			continue
		}
		ph, perr := normalizeWhatsappPhone(t.phone)
		if perr != nil || len(ph) != 12 {
			badPhone++
			continue
		}
		if seen[ph] {
			continue
		}
		seen[ph] = true
		t.phone = ph
		targets = append(targets, t)
	}
	return targets, noConsent, badPhone, rows.Err()
}

func validChannel(ch string) bool { return ch == "sms" || ch == "whatsapp" }

// PromoAudiencePreview counts who a campaign would reach.
func (s *Service) PromoAudiencePreview(seg PromoSegment, body string) (*PromoAudience, error) {
	targets, noConsent, bad, err := s.promoTargets(seg)
	if err != nil {
		return nil, err
	}
	a := &PromoAudience{Count: len(targets), NoConsent: noConsent, BadPhone: bad, Sample: []string{}}
	for i, t := range targets {
		if i < 5 {
			a.Sample = append(a.Sample, t.name)
		}
	}
	sampleName, pts := "Wanjiku", int64(120)
	if len(targets) > 0 {
		sampleName, pts = targets[0].name, targets[0].points
	}
	a.PreviewBody = s.personalise(body, sampleName, pts)
	a.Characters = utf8.RuneCountInString(a.PreviewBody)
	a.Segments = smsParts(a.PreviewBody)
	for _, t := range targets { // the longest message decides the cost
		if p := smsParts(s.personalise(body, t.name, t.points)); p > a.Segments {
			a.Segments = p
		}
	}
	return a, nil
}

// ---- campaigns ---------------------------------------------------------------

func (s *Service) CreatePromo(req CreatePromoRequest, p *auth.Principal) (*PromoCampaign, error) {
	req.Name, req.Body = strings.TrimSpace(req.Name), strings.TrimSpace(req.Body)
	if req.Name == "" || len(req.Name) > 80 {
		return nil, fmt.Errorf("%w: give the campaign a name (up to 80 characters)", ErrInvalidInput)
	}
	if !validChannel(req.Channel) {
		return nil, fmt.Errorf("%w: channel must be sms or whatsapp", ErrInvalidInput)
	}
	if req.Body == "" || utf8.RuneCountInString(req.Body) > 900 {
		return nil, fmt.Errorf("%w: the message must be 1 to 900 characters", ErrInvalidInput)
	}
	targets, _, _, err := s.promoTargets(req.Segment)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: no customer who agreed to offers matches", ErrInvalidInput)
	}
	if len(targets) > promoMaxRecipients {
		return nil, fmt.Errorf("%w: %d recipients, send to at most %d at a time", ErrInvalidInput, len(targets), promoMaxRecipients)
	}
	seg, _ := json.Marshal(req.Segment)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	uuid := randToken(12)
	if _, err := tx.Exec(s.db.Rebind(`INSERT INTO promo_campaigns (client_uuid, name, channel, body, segment_json, status, total, created_by_name, created_at)
		VALUES (?, ?, ?, ?, ?, 'draft', ?, ?, ?)`), uuid, req.Name, req.Channel, req.Body, string(seg), len(targets), p.Username, nowStamp()); err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(s.db.Rebind(`SELECT id FROM promo_campaigns WHERE client_uuid = ?`), uuid).Scan(&id); err != nil {
		return nil, err
	}
	for _, t := range targets {
		if _, err := tx.Exec(s.db.Rebind(`INSERT INTO promo_recipients (campaign_id, customer_id, name, phone, body, status) VALUES (?, ?, ?, ?, ?, 'pending')`),
			id, t.id, truncStr(t.name, 80), t.phone, s.personalise(req.Body, t.name, t.points)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "PROMO_CREATED", "promo", fmt.Sprint(id), fmt.Sprintf("%s via %s to %d", req.Name, req.Channel, len(targets)))
	return s.GetPromo(id)
}

func (s *Service) promoProviderReady(channel string) bool {
	if channel == "whatsapp" {
		return s.settings.Get("whatsapp_api_key") != ""
	}
	return s.settings.Get("sms_username") != "" && s.settings.Get("sms_api_key") != ""
}

func (s *Service) GetPromo(id int64) (*PromoCampaign, error) {
	var c PromoCampaign
	var seg string
	err := s.db.QueryRow(s.db.Rebind(`SELECT id, name, channel, body, segment_json, status, total, last_error, created_by_name, created_at, started_at, finished_at
		FROM promo_campaigns WHERE id = ?`), id).Scan(&c.ID, &c.Name, &c.Channel, &c.Body, &seg, &c.Status, &c.Total, &c.LastError,
		&c.CreatedBy, &c.CreatedAt, &c.StartedAt, &c.FinishedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	_ = json.Unmarshal([]byte(seg), &c.Segment)
	rows, err := s.db.Query(s.db.Rebind(`SELECT status, COUNT(*) FROM promo_recipients WHERE campaign_id = ? GROUP BY status`), id)
	if err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				switch st {
				case "sent":
					c.Sent += n
				case "failed":
					c.Failed += n
				case "pending", "sending":
					c.Pending += n
				}
			}
		}
		rows.Close()
	}
	var maxBody string
	_ = s.db.QueryRow(s.db.Rebind(`SELECT body FROM promo_recipients WHERE campaign_id = ? ORDER BY LENGTH(body) DESC LIMIT 1`), id).Scan(&maxBody)
	c.Segments = smsParts(maxBody)
	c.QuietNow = s.promoQuietNow(time.Now())
	c.ProviderSet = s.promoProviderReady(c.Channel)
	return &c, nil
}

func (s *Service) ListPromos() ([]PromoCampaign, error) {
	rows, err := s.db.Query(`SELECT id FROM promo_campaigns ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []PromoCampaign{}
	for _, id := range ids {
		if c, err := s.GetPromo(id); err == nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (s *Service) PromoRecipients(id int64) ([]PromoRecipient, error) {
	rows, err := s.db.Query(s.db.Rebind(`SELECT name, phone, status, error, sent_at FROM promo_recipients WHERE campaign_id = ? ORDER BY id LIMIT 5000`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromoRecipient{}
	for rows.Next() {
		var r PromoRecipient
		if rows.Scan(&r.Name, &r.Phone, &r.Status, &r.Error, &r.SentAt) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// PromoAction moves a campaign: start | pause | resume | cancel.
func (s *Service) PromoAction(id int64, action string, p *auth.Principal) (*PromoCampaign, error) {
	s.promoMu.Lock()
	defer s.promoMu.Unlock()
	c, err := s.GetPromo(id)
	if err != nil {
		return nil, err
	}
	now := nowStamp()
	switch action {
	case "start", "resume":
		if c.Status != "draft" && c.Status != "paused" {
			return nil, fmt.Errorf("%w: campaign is %s", ErrInvalidState, c.Status)
		}
		if !s.promoProviderReady(c.Channel) {
			return nil, fmt.Errorf("%w: set up %s sending in Settings → Promotions first", ErrNotConfigured, strings.ToUpper(c.Channel))
		}
		_, err = s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET status = 'sending', last_error = '', started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END WHERE id = ?`), now, id)
	case "pause":
		if c.Status != "sending" {
			return nil, fmt.Errorf("%w: campaign is %s", ErrInvalidState, c.Status)
		}
		_, err = s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET status = 'paused' WHERE id = ?`), id)
	case "cancel":
		if c.Status == "done" || c.Status == "cancelled" {
			return nil, fmt.Errorf("%w: campaign is %s", ErrInvalidState, c.Status)
		}
		if _, err = s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'skipped' WHERE campaign_id = ? AND status = 'pending'`), id); err == nil {
			_, err = s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET status = 'cancelled', finished_at = ? WHERE id = ?`), now, id)
		}
	default:
		return nil, fmt.Errorf("%w: action must be start, pause, resume or cancel", ErrInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "PROMO_"+strings.ToUpper(action), "promo", fmt.Sprint(id), c.Name)
	return s.GetPromo(id)
}

// ---- consent -----------------------------------------------------------------

// SetMarketingConsent records whether a customer agreed to offers.
func (s *Service) SetMarketingConsent(customerID int64, optIn bool, p *auth.Principal) error {
	var phone string
	if err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,'') FROM customers WHERE id = ?`), customerID).Scan(&phone); err != nil {
		return ErrNotFound
	}
	now := nowStamp()
	if _, err := s.db.Exec(s.db.Rebind(`UPDATE customers SET marketing_opt_in = ?, marketing_updated_at = ? WHERE id = ?`), btoi(optIn), now, customerID); err != nil {
		return err
	}
	if !optIn {
		s.skipPendingFor(phone)
	}
	s.Audit(p.ID, p.Username, "MARKETING_CONSENT", "customer", fmt.Sprint(customerID), fmt.Sprint(optIn))
	if phone != "" {
		s.Emit("consent", "upsert", map[string]any{"phone": phone, "optIn": optIn, "at": now})
	}
	return nil
}

// OptOutPhone is the "please stop" button: by phone number, whoever has it.
func (s *Service) OptOutPhone(raw string, p *auth.Principal) (int, error) {
	want, err := normalizeWhatsappPhone(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(phone,'') FROM customers WHERE COALESCE(phone,'') != ''`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var ph string
		if rows.Scan(&id, &ph) == nil {
			if n, e := normalizeWhatsappPhone(ph); e == nil && n == want {
				ids = append(ids, id)
			}
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := s.SetMarketingConsent(id, false, p); err != nil {
			return 0, err
		}
	}
	s.skipPendingFor(want)
	return len(ids), nil
}

func (s *Service) skipPendingFor(phone string) {
	if n, err := normalizeWhatsappPhone(phone); err == nil {
		s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'skipped', error = 'opted out' WHERE phone = ? AND status = 'pending'`), n)
	}
}

func (s *Service) applyConsent(payload json.RawMessage) error {
	var ev struct {
		Phone string `json:"phone"`
		OptIn bool   `json:"optIn"`
		At    string `json:"at"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return err
	}
	if ev.Phone == "" || ev.At == "" {
		return nil
	}
	_, err := s.db.Exec(s.db.Rebind(`UPDATE customers SET marketing_opt_in = ?, marketing_updated_at = ? WHERE phone = ? AND COALESCE(marketing_updated_at,'') < ?`),
		btoi(ev.OptIn), ev.At, ev.Phone, ev.At)
	if err == nil && !ev.OptIn {
		s.skipPendingFor(ev.Phone)
	}
	return err
}

// ---- sending -----------------------------------------------------------------

func hhmm(v string, def int) int {
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return def
	}
	return h*60 + m
}

// promoQuietNow: inside the quiet window (which may wrap past midnight).
func (s *Service) promoQuietNow(now time.Time) bool {
	start := hhmm(s.settings.Get("promo_quiet_start"), 20*60)
	end := hhmm(s.settings.Get("promo_quiet_end"), 8*60)
	if start == end {
		return false
	}
	l := now.In(eastAfrica)
	cur := l.Hour()*60 + l.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}

type promoSendResult struct {
	phone    string
	ok       bool
	retry    bool
	ref      string
	errMsg   string
	stopping bool // an account-level problem (no credit, bad sender): pause the campaign
}

// sendSMSBatch calls Africa's Talking once for one message to many numbers.
func (s *Service) sendSMSBatch(phones []string, body string) ([]promoSendResult, error) {
	if s.promoSMSForTest != nil {
		return s.promoSMSForTest(phones, body)
	}
	base := strings.TrimRight(s.settings.Get("sms_api_url"), "/")
	if base == "" {
		base = "https://api.africastalking.com"
		if s.settings.Get("sms_env") == "sandbox" || s.settings.Get("sms_username") == "sandbox" {
			base = "https://api.sandbox.africastalking.com"
		}
	}
	to := make([]string, len(phones))
	for i, p := range phones {
		to[i] = "+" + p
	}
	form := url.Values{}
	form.Set("username", s.settings.Get("sms_username"))
	form.Set("to", strings.Join(to, ","))
	form.Set("message", body)
	form.Set("bulkSMSMode", "1")
	if from := strings.TrimSpace(s.settings.Get("sms_sender_id")); from != "" {
		form.Set("from", from)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/version1/messaging", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apiKey", s.settings.Get("sms_api_key"))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Africa's Talking refused the username or API key (%d)", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Africa's Talking answered %d: %s", resp.StatusCode, truncStr(strings.TrimSpace(string(raw)), 160))
	}
	var out struct {
		SMSMessageData struct {
			Message    string `json:"Message"`
			Recipients []struct {
				Number     string `json:"number"`
				StatusCode int    `json:"statusCode"`
				Status     string `json:"status"`
				MessageID  string `json:"messageId"`
			} `json:"Recipients"`
		} `json:"SMSMessageData"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Africa's Talking answer not understood: %s", truncStr(string(raw), 160))
	}
	res := []promoSendResult{}
	for _, r := range out.SMSMessageData.Recipients {
		pr := promoSendResult{phone: strings.TrimPrefix(r.Number, "+"), ref: r.MessageID}
		switch r.StatusCode {
		case 100, 101, 102:
			pr.ok = true
		case 402, 405, 401:
			pr.stopping, pr.retry = true, true
			pr.errMsg = r.Status
		case 500, 501, 502:
			pr.retry, pr.errMsg = true, r.Status
		default:
			pr.errMsg = firstNonEmpty(r.Status, fmt.Sprint(r.StatusCode))
		}
		res = append(res, pr)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("Africa's Talking sent nothing: %s", truncStr(out.SMSMessageData.Message, 160))
	}
	return res, nil
}

func (s *Service) sendPromoWhatsapp(phone, body string) promoSendResult {
	var err error
	if s.promoWhatsappForTest != nil {
		err = s.promoWhatsappForTest(phone, body)
	} else {
		err = s.sendWhatsappText(phone, body)
	}
	if err != nil {
		return promoSendResult{phone: phone, retry: true, errMsg: truncStr(err.Error(), 200)}
	}
	return promoSendResult{phone: phone, ok: true}
}

// DrainPromos sends the next slice of every running campaign. Returns how
// many messages went out.
func (s *Service) DrainPromos(now time.Time) int {
	s.promoMu.Lock()
	defer s.promoMu.Unlock()
	// rows a stopped till left mid-send: never resend (it may have gone)
	stuck := now.Add(-promoStuckAfter).UTC().Format(nowStampLayout)
	s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'failed', error = 'result unknown (the till stopped while sending)' WHERE status = 'sending' AND next_try_at < ?`), stuck)
	if s.promoQuietNow(now) {
		return 0
	}
	rows, err := s.db.Query(`SELECT id, channel FROM promo_campaigns WHERE status = 'sending' ORDER BY id`)
	if err != nil {
		return 0
	}
	type camp struct {
		id int64
		ch string
	}
	var camps []camp
	for rows.Next() {
		var c camp
		if rows.Scan(&c.id, &c.ch) == nil {
			camps = append(camps, c)
		}
	}
	rows.Close()
	sent := 0
	for _, c := range camps {
		sent += s.drainCampaign(c.id, c.ch, now)
	}
	return sent
}

func (s *Service) drainCampaign(id int64, channel string, now time.Time) int {
	limit := promoSMSBatch * 3
	if channel == "whatsapp" {
		limit = promoWhatsappBatch
	}
	nowS := now.UTC().Format(nowStampLayout)
	rows, err := s.db.Query(s.db.Rebind(`SELECT id, phone, body FROM promo_recipients WHERE campaign_id = ? AND status = 'pending' AND next_try_at <= ? ORDER BY id LIMIT ?`), id, nowS, limit)
	if err != nil {
		return 0
	}
	type rcp struct {
		id          int64
		phone, body string
	}
	var batch []rcp
	for rows.Next() {
		var r rcp
		if rows.Scan(&r.id, &r.phone, &r.body) == nil {
			batch = append(batch, r)
		}
	}
	rows.Close()
	if len(batch) == 0 {
		var left int
		s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM promo_recipients WHERE campaign_id = ? AND status IN ('pending','sending')`), id).Scan(&left)
		if left == 0 {
			s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET status = 'done', finished_at = ? WHERE id = ? AND status = 'sending'`), nowS, id)
		}
		return 0
	}
	// claim the batch before calling anyone
	for _, r := range batch {
		s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'sending', attempts = attempts + 1, next_try_at = ? WHERE id = ? AND status = 'pending'`), nowS, r.id)
	}
	byPhone := map[string]int64{}
	for _, r := range batch {
		byPhone[r.phone] = r.id
	}
	apply := func(res []promoSendResult) (stop string) {
		for _, pr := range res {
			rid, ok := byPhone[pr.phone]
			if !ok {
				continue
			}
			delete(byPhone, pr.phone)
			switch {
			case pr.ok:
				s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'sent', provider_ref = ?, error = '', sent_at = ? WHERE id = ?`), truncStr(pr.ref, 80), nowS, rid)
			case pr.retry:
				s.retryOrFail(rid, pr.errMsg, now)
				if pr.stopping {
					stop = pr.errMsg
				}
			default:
				s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'failed', error = ? WHERE id = ?`), truncStr(pr.errMsg, 200), rid)
			}
		}
		return stop
	}
	sent := 0
	stop := ""
	if channel == "sms" {
		groups := map[string][]string{}
		var order []string
		for _, r := range batch {
			if _, ok := groups[r.body]; !ok {
				order = append(order, r.body)
			}
			groups[r.body] = append(groups[r.body], r.phone)
		}
		sort.SliceStable(order, func(i, j int) bool { return len(groups[order[i]]) > len(groups[order[j]]) })
		for _, body := range order {
			phones := groups[body]
			for i := 0; i < len(phones) && stop == ""; i += promoSMSBatch {
				end := i + promoSMSBatch
				if end > len(phones) {
					end = len(phones)
				}
				res, err := s.sendSMSBatch(phones[i:end], body)
				if err != nil {
					// the call itself failed: nothing in this slice went out
					for _, ph := range phones[i:end] {
						if rid, ok := byPhone[ph]; ok {
							s.retryOrFail(rid, err.Error(), now)
							delete(byPhone, ph)
						}
					}
					s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET last_error = ? WHERE id = ?`), truncStr(err.Error(), 200), id)
					continue
				}
				for _, r := range res {
					if r.ok {
						sent++
					}
				}
				stop = apply(res)
			}
		}
	} else {
		for i, r := range batch {
			if i > 0 && s.promoWhatsappForTest == nil {
				time.Sleep(promoWhatsappGap)
			}
			res := s.sendPromoWhatsapp(r.phone, r.body)
			if res.ok {
				sent++
			}
			apply([]promoSendResult{res})
		}
	}
	// anything claimed but not answered for goes back for a retry
	for _, rid := range byPhone {
		s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'pending' WHERE id = ? AND status = 'sending'`), rid)
	}
	if stop != "" {
		s.db.Exec(s.db.Rebind(`UPDATE promo_campaigns SET status = 'paused', last_error = ? WHERE id = ?`), truncStr("paused: "+stop+" (fix it in your SMS account, then resume)", 200), id)
	}
	return sent
}

func (s *Service) retryOrFail(rid int64, msg string, now time.Time) {
	var attempts int
	s.db.QueryRow(s.db.Rebind(`SELECT attempts FROM promo_recipients WHERE id = ?`), rid).Scan(&attempts)
	if attempts >= promoMaxAttempts {
		s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'failed', error = ? WHERE id = ?`), truncStr(msg, 200), rid)
		return
	}
	next := now.Add(time.Duration(attempts*attempts) * time.Minute).UTC().Format(nowStampLayout)
	s.db.Exec(s.db.Rebind(`UPDATE promo_recipients SET status = 'pending', error = ?, next_try_at = ? WHERE id = ?`), truncStr(msg, 200), next, rid)
}

// PromoTestSend sends one message to the owner's own phone.
func (s *Service) PromoTestSend(channel, phone, body string) error {
	ph, err := normalizeWhatsappPhone(phone)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if !s.promoProviderReady(channel) {
		return fmt.Errorf("%w: %s is not set up", ErrNotConfigured, channel)
	}
	text := s.personalise(firstNonEmpty(strings.TrimSpace(body), "Test from {shop}: promotions are set up."), "Owner", 0)
	if channel == "whatsapp" {
		if r := s.sendPromoWhatsapp(ph, text); !r.ok {
			return fmt.Errorf("WhatsApp: %s", r.errMsg)
		}
		return nil
	}
	res, err := s.sendSMSBatch([]string{ph}, text)
	if err != nil {
		return err
	}
	if !res[0].ok {
		return fmt.Errorf("SMS: %s", res[0].errMsg)
	}
	return nil
}

// PromoWorker runs DrainPromos on a timer (one per shop Service).
type PromoWorker struct{ svc *Service }

func NewPromoWorker(svc *Service) *PromoWorker { return &PromoWorker{svc: svc} }

func (w *PromoWorker) Run(ctx context.Context) {
	t := time.NewTicker(promoTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[promo] drain panic: %v", r)
					}
				}()
				w.svc.DrainPromos(now)
			}()
		}
	}
}
