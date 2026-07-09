// Package dlocal implementa el verificador y normalizador de webhooks dLocal.
package dlocal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// dlocalVerifier verifica la firma HMAC-SHA256 de webhooks dLocal.
// dLocal envía la firma en header X-Signature = HMAC-SHA256(X-Date + body, secret_key).
type dlocalVerifier struct{}

func (v *dlocalVerifier) Verify(r *http.Request, secret string) error {
	// Leer el body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	// Restaurar el body para que pueda ser leído de nuevo
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Obtener headers
	date := r.Header.Get("X-Date")
	signature := r.Header.Get("X-Signature")

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

	// Extraer datos del pago
	var paymentID string
	var amount core.Money
	var status core.PaymentStatus

	if wh.Data != nil {
		paymentID = wh.Data.ID
		amount = core.Money{
			Amount:   int64(wh.Data.Amount * 100),
			Currency: wh.Data.Currency,
		}
		status = mapPaymentStatus(wh.Data.Status)
	}

	return &webhook.NormalizedEvent{
		Provider:     Provider,
		EventType:    evtType,
		PaymentID:    paymentID,
		Status:       status,
		Amount:       amount,
		Raw:          wh,
		ProcessedAt:  time.Now().UTC(),
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

// mapPaymentStatus traduce estados de pago dLocal a PaymentStatus estándar.
func mapPaymentStatus(s string) core.PaymentStatus {
	switch s {
	case "PAID":
		return core.StatusCaptured
	case "AUTHORIZED":
		return core.StatusAuthorized
	case "PENDING":
		return core.StatusPending
	case "REJECTED":
		return core.StatusFailed
	case "CANCELLED":
		return core.StatusVoided
	case "REFUNDED":
		return core.StatusRefunded
	case "PARTIALLY_REFUNDED":
		return core.StatusPartiallyRefunded
	default:
		return core.StatusFailed
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
