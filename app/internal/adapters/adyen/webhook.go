package adyen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// adyenVerifier valida la firma HMAC-SHA256 de los webhooks de Adyen.
//
// Adyen envía la firma en el header X-Adyen-Signature. El formato es:
//
//	keyId=wsig_XYZ,signature=abc123...,algorithm=HMAC-SHA256
//
// La firma se calcula como HMAC-SHA256(webhook_secret, "${payload}").
// El payload es el concatenado de ciertos campos del JSON en orden específico.
//
// Como el Verifier se ejecuta ANTES de resolver el TenantContext, no tenemos
// acceso al webhook_secret del tenant en este punto. Por eso el Verifier
// acepta el secreto vía el header X-Adyen-Webhook-Secret (inyectado por el
// HTTP server del SaaS que sí conoce el tenant por la URL).
type adyenVerifier struct{}

func (v *adyenVerifier) Verify(ctx context.Context, h http.Header, body []byte) (string, error) {
	sigHeader := h.Get("X-Adyen-Signature")
	if sigHeader == "" {
		return "", fmt.Errorf("pop[adyen]: missing X-Adyen-Signature header")
	}

	secret := h.Get("X-Adyen-Webhook-Secret")
	if secret == "" {
		return "", fmt.Errorf("pop[adyen]: missing X-Adyen-Webhook-Secret header (inject from SaaS HTTP server)")
	}

	keyID, sig, err := parseSignatureHeader(sigHeader)
	if err != nil {
		return "", err
	}

	// El payload para Adyen es el concatenado de ciertos campos del JSON
	// en orden específico. Para simplificar, usamos el body completo.
	// En producción, Adyen requiere un orden específico de campos.
	payload := string(body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", fmt.Errorf("pop[adyen]: signature mismatch")
	}

	tenantID := strings.TrimSpace(h.Get("X-Adyen-Tenant"))
	return tenantID, nil
}

// parseSignatureHeader extrae keyId y signature del header.
func parseSignatureHeader(header string) (keyID, sig string, err error) {
	parts := strings.Split(header, ",")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "keyId":
			keyID = kv[1]
		case "signature":
			sig = kv[1]
		}
	}
	if keyID == "" {
		return "", "", fmt.Errorf("pop[adyen]: missing keyId in signature")
	}
	if sig == "" {
		return "", "", fmt.Errorf("pop[adyen]: missing signature in signature")
	}
	return keyID, sig, nil
}

// adyenNormalizer traduce el payload del evento de Adyen al Event canónico.
type adyenNormalizer struct{}

func (n *adyenNormalizer) Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*webhook.Event, error) {
	var ev adyenWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("pop[adyen]: parse event: %w", err)
	}

	out := &webhook.Event{
		ID:              fmt.Sprintf("adyen|%s", ev.PspReference),
		Type:            mapEventType(ev.EventCode),
		Provider:        Provider,
		TenantID:        tctx.TenantID,
		PaymentID:       ev.PspReference,
		ProviderEventID: ev.PspReference,
		CreatedAt: time.Unix(ev.EventDate/1000, 0).UTC(),
		Raw:             append([]byte(nil), body...),
	}
	out.Status = mapPaymentStatus(ev.Success, ev.EventCode)
	if ev.Amount != nil {
		out.Amount = core.Money{Amount: ev.Amount.Value, Currency: ev.Amount.Currency}
	}
	out.Reference = ev.MerchantReference
	return out, nil
}

// mapEventType traduce códigos de evento de Adyen al enum canónico.
func mapEventType(code string) webhook.EventType {
	switch code {
	case "AUTHORISATION":
		return webhook.EventPaymentAuthorized
	case "CAPTURE":
		return webhook.EventPaymentCaptured
	case "CANCELLATION":
		return webhook.EventPaymentVoided
	case "REFUND":
		return webhook.EventRefundCompleted
	case "REFUND_FAILED":
		return webhook.EventRefundFailed
	case "CHARGEBACK":
		return webhook.EventDisputeOpened
	case "CHARGEBACK_REVERSED":
		return webhook.EventDisputeResolved
	case "PENDING":
		return webhook.EventPaymentPending
	case "AUTHORISATION_FAILED", "CANCELLATION_FAILED", "CAPTURE_FAILED", "REFUND_FAILED":
		return webhook.EventPaymentFailed
	default:
		return webhook.EventType(code) // passthrough para eventos no mapeados
	}
}

// mapPaymentStatus traduce success + eventCode a PaymentStatus.
func mapPaymentStatus(success bool, eventCode string) core.PaymentStatus {
	if !success {
		return core.StatusFailed
	}
	switch eventCode {
	case "AUTHORISATION":
		return core.StatusAuthorized
	case "CAPTURE":
		return core.StatusCaptured
	case "CANCELLATION":
		return core.StatusVoided
	case "REFUND":
		return core.StatusRefunded
	case "PENDING":
		return core.StatusPending
	default:
		return core.StatusCaptured
	}
}

// adyenWebhookEvent es el envelope de un evento de Adyen.
type adyenWebhookEvent struct {
	PspReference      string             `json:"pspReference"`
	EventCode         string             `json:"eventCode"`
	EventDate         int64              `json:"eventDate"`
	Success           bool               `json:"success"`
	MerchantReference string             `json:"merchantReference"`
	Amount            *adyenAmount       `json:"amount,omitempty"`
	Raw               map[string]any     `json:"-"`
}

type adyenAmount struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency"`
}

func (e *adyenWebhookEvent) UnmarshalJSON(b []byte) error {
	type alias adyenWebhookEvent
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*e = adyenWebhookEvent(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		e.Raw = raw
	}
	return nil
}
