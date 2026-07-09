// Package kushki implementa verificación y normalización de webhooks de Kushki.
//
// Kushki envía webhooks con firma HMAC-SHA256 usando el secret del merchant.
// El formato de eventos es JSON con un campo "event" que indica el tipo.
package kushki

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// kushkiVerifier implementa webhook.Verifier para Kushki.
type kushkiVerifier struct{}

// Verify valida la firma del webhook de Kushki.
//
// Kushki envía la firma en el header `X-Kushki-Signature` como HMAC-SHA256
// del body en base64. El secret es el webhook secret del tenant.
func (v *kushkiVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sig := headers.Get("X-Kushki-Signature")
	if sig == "" {
		return fmt.Errorf("missing X-Kushki-Signature header")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// kushkiNormalizer implementa webhook.Normalizer para Kushki.
type kushkiNormalizer struct{}

// Normalize traduce un evento webhook de Kushki al formato canónico del SDK.
//
// Formato Kushki: {"event": "charge.success", "data": {...}}
func (n *kushkiNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var env struct {
		Event string `json:"event"`
		Data  struct {
			TicketNumber string  `json:"ticketNumber"`
			Status       string  `json:"status"`
			Amount       float64 `json:"amount"`
			Currency     string  `json:"currency"`
			CreatedAt    string  `json:"createdAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("unmarshal kushki webhook: %w", err)
	}

	// Mapear evento Kushki → evento canónico.
	var eventType string
	switch env.Event {
	case "charge.success":
		eventType = "payment.captured"
	case "charge.authorization":
		eventType = "payment.authorized"
	case "charge.declined":
		eventType = "payment.failed"
	case "charge.voided":
		eventType = "payment.voided"
	case "charge.pending":
		eventType = "payment.pending"
	case "refund.success":
		eventType = "refund.completed"
	case "refund.failed":
		eventType = "refund.failed"
	default:
		eventType = env.Event
	}

	// Construir payload canónico.
	payload := map[string]any{
		"provider":     Provider,
		"ticketNumber": env.Data.TicketNumber,
		"status":       env.Data.Status,
		"amount":       env.Data.Amount,
		"currency":     env.Data.Currency,
		"createdAt":    env.Data.CreatedAt,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     eventType,
		Payload:  payload,
		Raw:      body,
	}, nil
}

// --- helpers para tests ---

// BuildTestWebhook construye un webhook de prueba para Kushki.
func BuildTestWebhook(event string, ticketNumber string, status string, amount float64, currency string) []byte {
	env := map[string]any{
		"event": event,
		"data": map[string]any{
			"ticketNumber": ticketNumber,
			"status":       status,
			"amount":       amount,
			"currency":     currency,
			"createdAt":    "2025-01-02T15:04:05.000Z",
		},
	}
	body, _ := json.Marshal(env)
	return body
}

// SignWebhook firma un body con el secret dado (para tests).
func SignWebhook(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// --- helpers para server ---

// ParsePaymentID extrae el ticket number del payload del webhook.
func ParsePaymentID(payload map[string]any) (string, error) {
	ticket, ok := payload["ticketNumber"].(string)
	if !ok || ticket == "" {
		return "", fmt.Errorf("missing ticketNumber in payload")
	}
	return ticket, nil
}

// ParseAmount extrae el monto del payload del webhook.
func ParseAmount(payload map[string]any) (int64, string, error) {
	amount, ok := payload["amount"].(float64)
	if !ok {
		return 0, "", fmt.Errorf("missing amount in payload")
	}
	currency, ok := payload["currency"].(string)
	if !ok {
		return 0, "", fmt.Errorf("missing currency in payload")
	}
	return int64(amount * 100), currency, nil
}

// --- HTTP handler para el server ---

// HandleWebhook es el handler HTTP para webhooks de Kushki.
// Lee el body, verifica la firma, normaliza el evento y lo procesa.
func HandleWebhook(w http.ResponseWriter, r *http.Request, secret string, processor webhook.Processor) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	verifier := &kushkiVerifier{}
	if err := verifier.Verify(body, r.Header, secret); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	normalizer := &kushkiNormalizer{}
	evt, err := normalizer.Normalize(body)
	if err != nil {
		http.Error(w, "normalize webhook", http.StatusBadRequest)
		return
	}

	if err := processor(evt); err != nil {
		http.Error(w, "process webhook", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
