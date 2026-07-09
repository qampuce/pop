package mock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qampu/pop/internal/webhook"
)

// Webhook handler de referencia para el adapter mock.
//
// Esquema de firma (NO seguro, solo para tests/desarrollo):
//   - Header X-Mock-Signature: debe coincidir con el webhook secret del tenant.
//
// Payload esperado (JSON):
//
//	{
//	  "type":            "payment.captured",
//	  "payment_id":      "mock_pay_order_42",
//	  "reference":       "order_42",
//	  "status":          "captured",
//	  "amount":          19990,
//	  "currency":        "PEN",
//	  "provider_event_id": "evt_123",
//	  "created_at":      "2026-01-02T15:04:05Z"
//	}
func init() {
	webhook.Default.Register(Provider, &mockVerifier{}, &mockNormalizer{})
}

// mockVerifier acepta cualquier request con header X-Mock-Signature presente.
type mockVerifier struct{}

func (v *mockVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sig := headers.Get("X-Mock-Signature")
	if sig != "" && sig != secret {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// mockNormalizer traduce el payload JSON al formato canónico.
type mockNormalizer struct{}

func (n *mockNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var raw struct {
		Type            string `json:"type"`
		PaymentID       string `json:"payment_id"`
		Reference       string `json:"reference"`
		Status          string `json:"status"`
		Amount          int64  `json:"amount"`
		Currency        string `json:"currency"`
		ProviderEventID string `json:"provider_event_id"`
		CreatedAt       string `json:"created_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	createdAt, err := parseTime(raw.CreatedAt)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"provider":         Provider,
		"type":             raw.Type,
		"payment_id":       raw.PaymentID,
		"reference":        raw.Reference,
		"status":           raw.Status,
		"amount":           raw.Amount,
		"currency":         raw.Currency,
		"provider_event_id": raw.ProviderEventID,
		"created_at":       createdAt,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     raw.Type,
		Payload:  payload,
		Raw:      body,
	}, nil
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid created_at: %w", err)
	}
	return t.UTC(), nil
}
