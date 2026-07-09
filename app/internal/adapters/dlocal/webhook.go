// Package dlocal implementa el verificador y normalizador de webhooks dLocal.
package dlocal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/qampu/pop/internal/webhook"
)

// dlocalVerifier verifica la firma HMAC-SHA256 de webhooks dLocal.
// dLocal envía la firma en header X-Signature = HMAC-SHA256(X-Date + body, secret_key).
type dlocalVerifier struct{}

func (v *dlocalVerifier) Verify(body []byte, headers http.Header, secret string) error {
	// Obtener headers
	date := headers.Get("X-Date")
	signature := headers.Get("X-Signature")

	if date == "" || signature == "" {
		return fmt.Errorf("missing X-Date or X-Signature header")
	}

	// Calcular firma esperada
	expected := v.sign(date, body, secret)

	// Comparar firmas en tiempo constante para evitar timing attacks
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// sign genera la firma HMAC-SHA256 para dLocal.
// Firma = HMAC-SHA256(X-Date + body, secret_key)
func (v *dlocalVerifier) sign(date string, body []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(date))
	if len(body) > 0 {
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dlocalNormalizer convierte webhooks dLocal al formato estándar del SDK.
type dlocalNormalizer struct{}

func (n *dlocalNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var wh dlocalWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return nil, fmt.Errorf("unmarshal dlocal webhook: %w", err)
	}

	// Mapear tipo de evento dLocal a evento estándar
	evtType := mapEventType(wh.Type)
	if evtType == "" {
		return nil, fmt.Errorf("unknown dlocal event type: %s", wh.Type)
	}

	// Construir payload canónico.
	payload := map[string]any{
		"provider": Provider,
		"id":       wh.ID,
		"type":     wh.Type,
		"createdAt": wh.CreatedAt,
	}

	if wh.Data != nil {
		payload["payment_id"] = wh.Data.ID
		payload["status"] = wh.Data.Status
		payload["amount"] = wh.Data.Amount
		payload["currency"] = wh.Data.Currency
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     evtType,
		Payload:  payload,
		Raw:      body,
	}, nil
}

// mapEventType traduce tipos de evento dLocal a eventos estándar.
func mapEventType(t string) string {
	switch t {
	case "payment.paid":
		return "payment.captured"
	case "payment.authorized":
		return "payment.authorized"
	case "payment.rejected":
		return "payment.failed"
	case "payment.cancelled":
		return "payment.voided"
	case "payment.pending":
		return "payment.pending"
	case "refund.success":
		return "refund.completed"
	case "refund.failed":
		return "refund.failed"
	default:
		return ""
	}
}

// --- tipos crudos de webhooks dLocal ---

type dlocalWebhook struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	CreatedAt string      `json:"created_at"`
	Data      *dlocalData `json:"data,omitempty"`
}

type dlocalData struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}
