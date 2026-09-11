// Package mpesa abstracts M-Pesa payments behind a Provider interface so
// the POS never hard-depends on Daraja. Modes:
//   - auto   : STK push first, manual receipt-code fallback
//   - stk    : force STK push
//   - manual : customer pays to the till/paybill; cashier enters the
//              10-character receipt code (first-class flow, not an afterthought)
//
// Providers: Mock (demos/tests), Daraja (sandbox/production). Aggregators
// (KopoKopo, IntaSend, ...) can be added later behind the same interface.
package mpesa

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrInvalidPhone      = errors.New("invalid phone number")
	ErrInvalidReceipt    = errors.New("receipt code must be exactly 10 uppercase letters/digits")
	receiptPattern       = regexp.MustCompile(`^[A-Z0-9]{10}$`)
)

// STKRequest initiates a push.
type STKRequest struct {
	OrderID          int64
	PaymentID        int64
	Phone            string // normalized 2547XXXXXXXX / 2541XXXXXXXX
	AmountCents      int64
	AccountReference string // order number shown on the customer's phone
	Description      string
	CallbackURL      string // optional; polling works on LAN without webhooks
}

// STKResponse is the provider's synchronous answer.
type STKResponse struct {
	MerchantRequestID string
	CheckoutRequestID string
	CustomerMessage   string
}

// STKQueryResult is the async outcome.
type STKQueryResult struct {
	ResultCode         int    // 0 = success
	ResultDesc         string
	MpesaReceiptNumber string // 10-char alphanumeric, e.g. NLJ7RT61SV
	AmountCents        int64  // when the provider reports it (callbacks)
}

// Provider is the swappable payment backend.
type Provider interface {
	Name() string
	InitiateSTK(ctx context.Context, req STKRequest) (STKResponse, error)
	QuerySTK(ctx context.Context, checkoutRequestID string) (STKQueryResult, error)
}

// NormalizePhone converts Kenyan inputs (07xx, 7xx, +2547xx, 2547xx, 01xx)
// to the 12-digit 254XXXXXXXXX form.
func NormalizePhone(in string) (string, error) {
	p := strings.TrimSpace(in)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "-", "")
	if p == "" {
		return "", ErrInvalidPhone
	}
	switch {
	case strings.HasPrefix(p, "+254"):
		p = "254" + p[4:]
	case strings.HasPrefix(p, "254"):
		// already
	case strings.HasPrefix(p, "0"):
		p = "254" + p[1:]
	case len(p) == 9 && (strings.HasPrefix(p, "7") || strings.HasPrefix(p, "1")):
		p = "254" + p
	}
	if len(p) != 12 || !strings.HasPrefix(p, "254") || (!strings.HasPrefix(p, "2547") && !strings.HasPrefix(p, "2541")) {
		return "", ErrInvalidPhone
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return "", ErrInvalidPhone
		}
	}
	return p, nil
}

// ValidateReceiptCode enforces the 10-char alphanumeric M-Pesa receipt code.
func ValidateReceiptCode(code string) error {
	code = strings.TrimSpace(strings.ToUpper(code))
	if !receiptPattern.MatchString(code) {
		return ErrInvalidReceipt
	}
	return nil
}

// CanonicalReceipt uppercases and trims for storage.
func CanonicalReceipt(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// GenerateMockReceipt builds a valid-format receipt for the mock provider.
func GenerateMockReceipt(seq int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// Deterministic-ish but varied; MO## prefix keeps it obviously fake.
	s := []byte("MOCK")
	for i := 0; i < 6; i++ {
		s = append(s, alphabet[(seq+i*7+int('A'))%len(alphabet)])
	}
	return string(s)
}
