package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// Webhook handler de referencia para el adapter mock.
//
// Esquema de firma (NO seguro, solo para tests/desarrollo):
//   - Header X-Mock-Tenant:  tenantID destinatario.
//   - Header X-Mock-Signature: debe coincidir con TenantContext.WebhookSecret
//     del tenant resuelto. Como el Verifier se ejecuta ANTES de resolver el
//     TenantContext, aceptamos el valor "mock_secret" como convenio del
//     entorno de test, o cualquier valor si el header está presente.
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
	webhook.Default.Register(&webhook.WebhookHandler{
		Provider:  Provider,
		Verifier:  &mockVerifier{},
		Normalize: &mockNormalizer{},
	})
}

// mockVerifier acepta cualquier request con header X-Mock-Tenant presente.
// El tenantID se toma del header. La firma se valida contra un secreto
// convención "mock_secret" si está presente X-Mock-Signature.
type mockVerifier struct{}

func (v *mockVerifier) Verify(ctx context.Context, h http.Header, body []byte) (string, error) {
	tenantID := strings.TrimSpace(h.Get("X-Mock-Tenant"))
	if tenantID == "" {
		return "", fmt.Errorf("pop[mock]: missing X-Mock-Tenant header")
	}
	if sig := h.Get("X-Mock-Signature"); sig != "" && sig != "mock_secret" {
		return "", fmt.Errorf("pop[mock]: invalid signature")
	}
	return tenantID, nil
}

// mockNormalizer traduce el payload JSON al Event canónico.
type mockNormalizer struct{}

func (n *mockNormalizer) Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*webhook.Event, error) {
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
		return nil, fmt.Errorf("pop[mock]: parse body: %w", err)
	}

	createdAt, err := parseTime(raw.CreatedAt)
	if err != nil {
		return nil, err
	}

	ev := &webhook.Event{
		ID:              fmt.Sprintf("mock|%s", raw.ProviderEventID),
		Type:            webhook.EventType(raw.Type),
		PaymentID:       raw.PaymentID,
		Reference:       raw.Reference,
		Status:          core.PaymentStatus(raw.Status),
		Amount:          core.Money{Amount: raw.Amount, Currency: raw.Currency},
		ProviderEventID: raw.ProviderEventID,
		CreatedAt:       createdAt,
		Raw:             append([]byte(nil), body...),
	}
	return ev, nil
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("pop[mock]: invalid created_at: %w", err)
	}
	return t.UTC(), nil
}
