package mercadopago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/webhook"
)

// mpVerifier valida la firma HMAC-SHA256 de los webhooks de Mercado Pago.
//
// Header x-signature con formato:
//
//	ts=1614556800,v1=abc123...
//
// La firma v1 es HMAC-SHA256(webhook_secret, "ts:<ts>:data.id:<dataId>")
// donde dataId es el ID del recurso notificado (extraído del query param
// data.id o del body).
type mpVerifier struct{}

func (v *mpVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sigHeader := headers.Get("X-Signature")
	if sigHeader == "" {
		// Algunas integraciones viejas usan x-signature en lowercase.
		sigHeader = headers.Get("x-signature")
	}
	if sigHeader == "" {
		return fmt.Errorf("missing X-Signature header")
	}

	ts, sigs, err := parseMPSignature(sigHeader)
	if err != nil {
		return err
	}

	// Tolerancia de replay (5 min).
	if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return fmt.Errorf("signature timestamp out of tolerance")
	}

	// data.id: viene en el query param ?data.id=... o en el body como
	// resource o data.id. MP firma "ts:<ts>:data.id:<dataId>".
	dataID := headers.Get("X-MP-Data-ID")
	if dataID == "" {
		dataID = extractDataID(body)
	}

	manifest := fmt.Sprintf("ts:%d:data.id:%s", ts, dataID)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(manifest))
	expected := hex.EncodeToString(mac.Sum(nil))

	matched := false
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(expected)) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// parseMPSignature extrae ts y la lista de firmas v1 del header.
func parseMPSignature(header string) (ts int64, v1s []string, err error) {
	parts := strings.Split(header, ",")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "ts":
			ts, err = strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("pop[mercadopago]: invalid timestamp: %w", err)
			}
		case "v1":
			v1s = append(v1s, kv[1])
		}
	}
	if ts == 0 {
		return 0, nil, fmt.Errorf("pop[mercadopago]: missing timestamp in signature")
	}
	if len(v1s) == 0 {
		return 0, nil, fmt.Errorf("pop[mercadopago]: missing v1 signature")
	}
	return ts, v1s, nil
}

// extractDataID intenta obtener el data.id del body del webhook.
// MP envía {"action": "...", "data": {"id": "123"}} o
// {"resource": "https://api.mercadopago.com/v1/payments/123"}.
func extractDataID(body []byte) string {
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	if env.Data.ID != "" {
		return env.Data.ID
	}
	if env.Resource != "" {
		// "https://api.mercadopago.com/v1/payments/123" → "123"
		idx := strings.LastIndex(env.Resource, "/")
		if idx >= 0 && idx < len(env.Resource)-1 {
			return env.Resource[idx+1:]
		}
	}
	return ""
}

// mpNormalizer traduce el payload del evento de Mercado Pago al formato canónico.
type mpNormalizer struct{}

func (n *mpNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var ev mpWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("parse event: %w", err)
	}

	paymentID := ev.Data.ID
	if paymentID == "" {
		paymentID = extractDataID(body)
	}

	// Mapear acción MP → evento canónico.
	eventType := mapMPAction(ev.Action)

	// Construir payload canónico.
	payload := map[string]any{
		"provider":   Provider,
		"action":     ev.Action,
		"payment_id": paymentID,
		"event_id":   ev.ID,
		"type":       ev.Type,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     eventType,
		Payload:  payload,
		Raw:      body,
	}, nil
}

// mapMPAction traduce actions de MP al evento canónico.
func mapMPAction(action string) string {
	switch action {
	case "payment.created", "payment.updated":
		return "payment.pending"
	case "payment.approved":
		return "payment.captured"
	case "payment.rejected":
		return "payment.failed"
	case "payment.cancelled":
		return "payment.voided"
	case "payment.refunded":
		return "refund.completed"
	case "payment.partial_refunded":
		return "refund.completed"
	case "payment.authorized":
		return "payment.authorized"
	case "payment.in_process":
		return "payment.pending"
	case "merchant_order.created", "merchant_order.updated":
		return "payment.pending"
	case "dispute.created", "dispute.opened":
		return "dispute.opened"
	case "dispute.closed", "dispute.resolved":
		return "dispute.resolved"
	default:
		return action
	}
}

// mpWebhookEvent es el envelope de un evento de Mercado Pago.
type mpWebhookEvent struct {
	ID          int64  `json:"id"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	DateCreated string `json:"date_created"`
	UserID      int64  `json:"user_id"`
	Data        struct {
		ID string `json:"id"`
	} `json:"data"`
}
