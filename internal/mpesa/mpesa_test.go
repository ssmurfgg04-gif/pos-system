package mpesa

import (
        "encoding/json"
        "strings"
        "testing"
        "time"
)

func TestNormalizePhone(t *testing.T) {
        cases := map[string]string{
                "0722123456":  "254722123456",
                "722123456":   "254722123456",
                "+254722123456": "254722123456",
                "254722123456": "254722123456",
                "0110123456":  "254110123456",
                "0722 123 456": "254722123456",
                "0722-123-456": "254722123456",
        }
        for in, want := range cases {
                got, err := NormalizePhone(in)
                if err != nil || got != want {
                        t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
                }
        }
        bad := []string{"", "12345", "07221234567", "254822123456", "abc", "+255722123456", "0722abc456"}
        for _, in := range bad {
                if _, err := NormalizePhone(in); err == nil {
                        t.Errorf("NormalizePhone(%q) should fail", in)
                }
        }
}

func TestValidateReceiptCode(t *testing.T) {
        good := []string{"NLJ7RT61SV", "UC3HW8HDN2", "SQ31KS0VV2", "MOCK12AB3X"}
        for _, c := range good {
                if err := ValidateReceiptCode(c); err != nil {
                        t.Errorf("ValidateReceiptCode(%q) should pass: %v", c, err)
                }
        }
        bad := []string{"", "NLJ7RT61S", "NLJ7RT61SVV", "nlj7rt61sv!", "12345 6789", "NLJ7RT61S!"}
        for _, c := range bad {
                if err := ValidateReceiptCode(c); err == nil {
                        t.Errorf("ValidateReceiptCode(%q) should fail", c)
                }
        }
        // Mixed case is canonicalized (ToUpper) before validation — lenient in.
        if err := ValidateReceiptCode("nlj7rt61sv"); err != nil {
                t.Errorf("mixed case should canonicalize and pass: %v", err)
        }
        // CanonicalReceipt uppercases so mixed case is accepted via the handler.
        if CanonicalReceipt("nlj7rt61sv") != "NLJ7RT61SV" {
                t.Error("CanonicalReceipt should uppercase and trim")
        }
}

// Real captured production callback shape (Tuma log sample, names intact
// from the Daraja spec). Body.stkCallback nested structure.
const darajaCallbackSample = `{
  "Body": {
    "stkCallback": {
      "MerchantRequestID": "29115-34620561-1",
      "CheckoutRequestID": "ws_CO_191220191020363925",
      "ResultCode": 0,
      "ResultDesc": "The service request is processed successfully.",
      "CallbackMetadata": {
        "Item": [
          {"Name": "Amount", "Value": 1.00},
          {"Name": "MpesaReceiptNumber", "Value": "NLJ7RT61SV"},
          {"Name": "Balance"},
          {"Name": "PhoneNumber", "Value": 254722123456}
        ]
      }
    }
  }
}`

func TestParseCallbackSuccess(t *testing.T) {
        cb, err := ParseCallback([]byte(darajaCallbackSample))
        if err != nil {
                t.Fatalf("parse: %v", err)
        }
        if cb.CheckoutRequestID != "ws_CO_191220191020363925" {
                t.Errorf("checkout id: %s", cb.CheckoutRequestID)
        }
        if cb.ResultCode != 0 {
                t.Errorf("result code: %d", cb.ResultCode)
        }
        if cb.MpesaReceipt != "NLJ7RT61SV" {
                t.Errorf("receipt: %s", cb.MpesaReceipt)
        }
        if cb.AmountCents != 100 {
                t.Errorf("amount cents: %d (want 100)", cb.AmountCents)
        }
        if cb.Phone != "254722123456" {
                t.Errorf("phone: %s", cb.Phone)
        }
}

func TestParseCallbackCancelled(t *testing.T) {
        body := `{"Body":{"stkCallback":{"MerchantRequestID":"29115-34620561-1",
                "CheckoutRequestID":"ws_CO_X","ResultCode":1032,
                "ResultDesc":"Request cancelled by user"}}}`
        cb, err := ParseCallback([]byte(body))
        if err != nil {
                t.Fatalf("parse: %v", err)
        }
        if cb.ResultCode != 1032 || cb.MpesaReceipt != "" {
                t.Errorf("cancelled callback mis-parsed: %+v", cb)
        }
}

func TestParseCallbackRejects(t *testing.T) {
        bad := []string{``, `not json`, `{"Body":{}}`, `{"Body":{"stkCallback":{"ResultCode":0}}}`}
        for _, b := range bad {
                if _, err := ParseCallback([]byte(b)); err == nil {
                        t.Errorf("ParseCallback(%q) should fail", b)
                }
        }
}

func TestGenerateMockReceipt(t *testing.T) {
        for i := 1; i <= 20; i++ {
                r := GenerateMockReceipt(i)
                if err := ValidateReceiptCode(r); err != nil {
                        t.Fatalf("generated receipt %q invalid: %v", r, err)
                }
                if !strings.HasPrefix(r, "MOCK") {
                        t.Fatalf("mock receipt should be obvious: %s", r)
                }
        }
}

func TestMockProviderFlow(t *testing.T) {
        m := NewMock(30*time.Millisecond, 0)
        resp, err := m.InitiateSTK(nil, STKRequest{Phone: "254722123456", AmountCents: 55000})
        if err != nil {
                t.Fatalf("initiate: %v", err)
        }
        if resp.CheckoutRequestID == "" {
                t.Fatal("no checkout id")
        }
        // Immediately: pending.
        res, err := m.QuerySTK(nil, resp.CheckoutRequestID)
        if err != nil {
                t.Fatalf("query: %v", err)
        }
        if res.ResultCode != -1 {
                t.Errorf("expected pending (-1), got %d", res.ResultCode)
        }
        // After the delay: success with valid receipt.
        time.Sleep(60 * time.Millisecond)
        res, err = m.QuerySTK(nil, resp.CheckoutRequestID)
        if err != nil {
                t.Fatalf("query 2: %v", err)
        }
        if res.ResultCode != 0 || res.MpesaReceiptNumber == "" {
                t.Errorf("expected success, got %+v", res)
        }
        if err := ValidateReceiptCode(res.MpesaReceiptNumber); err != nil {
                t.Errorf("mock receipt invalid: %v", err)
        }
        // Third query: expired.
        if _, err := m.QuerySTK(nil, resp.CheckoutRequestID); err == nil {
                t.Error("third query should fail (push consumed)")
        }
}

func TestMockProviderFailure(t *testing.T) {
        m := NewMock(20*time.Millisecond, 1032)
        resp, _ := m.InitiateSTK(nil, STKRequest{Phone: "254722123456"})
        time.Sleep(40 * time.Millisecond)
        res, err := m.QuerySTK(nil, resp.CheckoutRequestID)
        if err != nil {
                t.Fatalf("query: %v", err)
        }
        if res.ResultCode != 1032 {
                t.Errorf("expected 1032, got %d", res.ResultCode)
        }
}

func TestDarajaPasswordFormat(t *testing.T) {
        // password = base64(shortcode + passkey + timestamp YYYYMMDDHHMMSS)
        d := NewDaraja("sandbox", "174379", "bfb279f9aa9bdbcf158e97dd71a467cd2e0c893058b10ded782b7157", "k", "s", "")
        ts := d.timestamp()
        if len(ts) != 14 {
                t.Errorf("timestamp %q not YYYYMMDDHHMMSS", ts)
        }
        if _, err := time.Parse("20060102150405", ts); err != nil {
                t.Errorf("timestamp unparseable: %v", err)
        }
        pw := d.password(ts)
        if pw == "" || strings.Contains(pw, " ") {
                t.Error("password must be non-empty base64")
        }
}

func TestCallbackJSONMarshal(t *testing.T) {
        // The webhook handler reads raw bodies; ensure the sample is valid JSON.
        if !json.Valid([]byte(darajaCallbackSample)) {
                t.Fatal("sample is not valid JSON")
        }
}
