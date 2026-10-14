package services

import (
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "testing"
)

// TestPaystackChargeCloudProxyMapping — the M-Pesa STK charge routes through
// the paystack-charge Edge Function when the till holds no local key, and
// Paystack's answer maps onto the same ChargeResult the local path produces.
func TestPaystackChargeCloudProxyMapping(t *testing.T) {
        mux := http.NewServeMux()
        var self string
        mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode([]map[string]any{{
                        "project_url": self, "team_code": "TEAM-A", "auto_approve": true,
                }})
        })
        mux.HandleFunc("/functions/v1/paystack-charge", func(w http.ResponseWriter, r *http.Request) {
                var in map[string]any
                json.NewDecoder(r.Body).Decode(&in)
                mm, _ := in["mobile_money"].(map[string]any)
                if in["p_device_id"] == "" || in["amount"] != float64(50000) {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                if mm == nil || mm["phone"] != "+254712345678" || mm["provider"] != "mpesa" {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "mobile money payload missing"})
                        return
                }
                json.NewEncoder(w).Encode(map[string]any{
                        "ok":           true,
                        "reference":    "LP-ORD1-ab12",
                        "display_text": "Please complete authorization process on your mobile phone",
                        "status":       "pay_offline",
                })
        })
        srv := httptest.NewServer(mux)
        defer srv.Close()
        self = srv.URL
        oldURL := cloudBaseURL
        cloudBaseURL = srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)
        // The owner flipped paystack_via_cloud (live since the v1.3.3 cutover):
        // no local key, proxy armed — the exact production till state.
        s.settings.Set("paystack_via_cloud", "true")
        // No local key configured: GetPaystack() is nil, so the charge MUST
        // ride the proxy (this is the exact post-cutover till state).
        if s.GetPaystack() != nil {
                t.Fatal("local paystack client unexpectedly configured")
        }
        ch, err := s.paystackCloudCharge("customer@ledgerpos.app", 50000, "KES", "LP-ORD1-ab12", "+254712345678", nil)
        if err != nil {
                t.Fatalf("proxy charge: %v", err)
        }
        if ch.Reference != "LP-ORD1-ab12" || ch.Status != "pay_offline" {
                t.Fatalf("charge mapping = %+v", ch)
        }
        if ch.DisplayText == "" {
                t.Fatal("display_text lost in proxy mapping")
        }

        // MpesaRoute must stay on the real-money STK path for a keyless till
        // (paystackConfigured now honors the cloud proxy).
        if !s.paystackConfigured() {
                t.Fatal("paystackConfigured false without local key — M-Pesa would degrade to manual")
        }
        if route := s.MpesaRoute(); route != "paystack" {
                t.Fatalf("MpesaRoute = %q, want paystack (proxy-armed)", route)
        }
}

// TestPaystackChargeProxyErrorPropagates — a Paystack-side failure surfaces
// as an error (the payment leg fails; nothing is left silently pending).
func TestPaystackChargeProxyErrorPropagates(t *testing.T) {
        mux := http.NewServeMux()
        var self string
        mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode([]map[string]any{{
                        "project_url": self, "team_code": "TEAM-A", "auto_approve": true,
                }})
        })
        mux.HandleFunc("/functions/v1/paystack-charge", func(w http.ResponseWriter, r *http.Request) {
                json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Invalid phone number format"})
        })
        srv := httptest.NewServer(mux)
        defer srv.Close()
        self = srv.URL
        oldURL := cloudBaseURL
        cloudBaseURL = srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)
        _, err := s.paystackCloudCharge("customer@ledgerpos.app", 50000, "KES", "LP-ORD1-ab12", "bad-phone", nil)
        if err == nil {
                t.Fatal("expected error for paystack-side rejection")
        }
}
