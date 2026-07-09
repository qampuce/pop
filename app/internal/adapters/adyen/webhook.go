package adyen

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
type adyenVerifier struct{}

func (v *adyenVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sigHeader := headers.Get("X-Adyen-Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing X-Adyen-Signature header")
	}

	keyID, sig, err := parseSignatureHeader(sigHeader)
	if err != nil {
		return err
	}

	// El payload para Adyen es el concatenado de ciertos campos del JSON
	// en orden específico según la documentación de Adyen:
	// https://docs.adyen.com/development-resources/webhooks/verify-hmac-signatures
	payload, err := buildWebhookPayload(body)
	if err != nil {
		return fmt.Errorf("build payload: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return fmt.Errorf("signature mismatch")
	}

	_ = keyID // keyID se puede usar para logging o auditoría
	return nil
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
		return "", "", fmt.Errorf("missing keyId in signature")
	}
	if sig == "" {
		return "", "", fmt.Errorf("missing signature in signature")
	}
	return keyID, sig, nil
}

// buildWebhookPayload construye el payload para la firma HMAC según la
// especificación de Adyen. El payload es el concatenado de ciertos campos
// del JSON en orden específico, separados por comas.
func buildWebhookPayload(body []byte) (string, error) {
	var ev adyenWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}

	// Según la documentación de Adyen, el payload es:
	// pspReference, originalReference, merchantAccountCode, merchantReference,
	// value, currency, eventCode, success
	// Donde value es el monto en la unidad mínima (cents)
	
	var amountValue string
	var currency string
	if ev.Amount != nil {
		amountValue = fmt.Sprintf("%d", ev.Amount.Value)
		currency = ev.Amount.Currency
	}

	payload := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s",
		ev.PspReference,
		ev.OriginalReference,
		ev.MerchantAccountCode,
		ev.MerchantReference,
		amountValue,
		currency,
		ev.EventCode,
		fmt.Sprintf("%t", ev.Success),
	)

	return payload, nil
}

// adyenNormalizer traduce el payload del evento de Adyen al formato canónico.
type adyenNormalizer struct{}

func (n *adyenNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var ev adyenWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("parse event: %w", err)
	}

	// Mapear evento Adyen → evento canónico.
	var eventType string
	switch ev.EventCode {
	case "AUTHORISATION":
		eventType = "payment.authorized"
	case "CAPTURE":
		eventType = "payment.captured"
	case "CANCELLATION":
		eventType = "payment.voided"
	case "REFUND":
		eventType = "refund.completed"
	case "REFUND_FAILED":
		eventType = "refund.failed"
	case "CHARGEBACK":
		eventType = "dispute.opened"
	case "CHARGEBACK_REVERSED":
		eventType = "dispute.resolved"
	case "PENDING":
		eventType = "payment.pending"
	case "AUTHORISATION_FAILED", "CANCELLATION_FAILED", "CAPTURE_FAILED":
		eventType = "payment.failed"
	default:
		eventType = ev.EventCode
	}

	// Construir payload canónico.
	payload := map[string]any{
		"provider":           Provider,
		"pspReference":       ev.PspReference,
		"eventCode":          ev.EventCode,
		"success":            ev.Success,
		"merchantReference":  ev.MerchantReference,
		"eventDate":          ev.EventDate,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     eventType,
		Payload:  payload,
		Raw:      body,
	}, nil
}

type adyenAmount struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency"`
}

// adyenWebhookEvent es el envelope de un evento de Adyen.
type adyenWebhookEvent struct {
	PspReference       string       `json:"pspReference"`
	OriginalReference  string       `json:"originalReference,omitempty"`
	MerchantAccountCode string       `json:"merchantAccountCode,omitempty"`
	MerchantReference  string       `json:"merchantReference"`
	EventCode          string       `json:"eventCode"`
	EventDate          int64        `json:"eventDate"`
	Success            bool         `json:"success"`
	Amount             *adyenAmount `json:"amount,omitempty"`
}
