package mpesa

// callback.go — the shared async-callback decoder. Extracted from the
// retired Daraja provider (v1.4.4): Buni posts the SAME Daraja-shaped
// STK callback envelope, so the public M-Pesa webhook and the Buni relay
// both parse through here. The parser is provider-agnostic — keep it
// even though the Daraja push provider itself is gone.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// CallbackResult is the parsed async webhook body (Daraja nests everything).
type CallbackResult struct {
	MerchantRequestID string
	CheckoutRequestID string
	ResultCode        int
	ResultDesc        string
	AmountCents       int64
	MpesaReceipt      string
	Phone             string
}

// ParseCallback decodes the real Daraja callback shape:
// {"Body":{"stkCallback":{"MerchantRequestID":...,"CheckoutRequestID":...,
//
//	"ResultCode":0,"ResultDesc":"...","CallbackMetadata":{"Item":[
//	{"Name":"Amount","Value":1},{"Name":"MpesaReceiptNumber","Value":"NLJ7RT61SV"},...]}}}}
func ParseCallback(body []byte) (CallbackResult, error) {
	var raw struct {
		Body struct {
			StkCallback struct {
				MerchantRequestID string `json:"MerchantRequestID"`
				CheckoutRequestID string `json:"CheckoutRequestID"`
				ResultCode        int    `json:"ResultCode"`
				ResultDesc        string `json:"ResultDesc"`
				CallbackMetadata  *struct {
					Item []struct {
						Name  string `json:"Name"`
						Value any    `json:"Value"`
					} `json:"Item"`
				} `json:"CallbackMetadata"`
			} `json:"stkCallback"`
		} `json:"Body"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return CallbackResult{}, err
	}
	cb := raw.Body.StkCallback
	if cb.CheckoutRequestID == "" {
		return CallbackResult{}, errors.New("callback: missing CheckoutRequestID")
	}
	res := CallbackResult{
		MerchantRequestID: cb.MerchantRequestID,
		CheckoutRequestID: cb.CheckoutRequestID,
		ResultCode:        cb.ResultCode,
		ResultDesc:        cb.ResultDesc,
	}
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			switch item.Name {
			case "Amount":
				if v, ok := item.Value.(float64); ok {
					res.AmountCents = int64(v * 100)
				}
			case "MpesaReceiptNumber":
				if v, ok := item.Value.(string); ok {
					res.MpesaReceipt = v
				}
			case "PhoneNumber":
				if v, ok := item.Value.(float64); ok {
					res.Phone = fmt.Sprintf("%.0f", v)
				}
			}
		}
	}
	return res, nil
}
