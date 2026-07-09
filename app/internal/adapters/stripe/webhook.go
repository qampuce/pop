package stripe

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

// stripeVerifier valida la firma HMAC-SHA256 de los webhooks de Stripe.
//
// Header Stripe-Signature con formato:
//
//	t=1614556800,v1=abc123...,v0=def456...
//
// La firma v1 es HMAC-SHA256(webhook_secret, "${t}.${body}"). Se compara en
// tiempo constante.
type stripeVerifier struct{}

func (v *stripeVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sigHeader := headers.Get("Stripe-Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing Stripe-Signature header")
	}

	ts, sigs, err := parseSignatureHeader(sigHeader)
	if err != nil {
		return err
	}

	// Tolerancia de replay (5 min).
	if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return fmt.Errorf("signature timestamp out of tolerance")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", ts, body)))
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

// parseSignatureHeader extrae t y la lista de firmas v1 del header.
func parseSignatureHeader(header string) (ts int64, v1s []string, err error) {
	parts := strings.Split(header, ",")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, err = strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("pop[stripe]: invalid timestamp: %w", err)
			}
		case "v1":
			v1s = append(v1s, kv[1])
		}
	}
	if ts == 0 {
		return 0, nil, fmt.Errorf("pop[stripe]: missing timestamp in signature")
	}
	if len(v1s) == 0 {
		return 0, nil, fmt.Errorf("pop[stripe]: missing v1 signature")
	}
	return ts, v1s, nil
}

// stripeNormalizer traduce el payload del evento de Stripe al formato canónico.
type stripeNormalizer struct{}

func (n *stripeNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var ev stripeWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("parse event: %w", err)
	}

	// Mapear tipo de evento Stripe → evento canónico.
	eventType := mapStripeEventType(ev.Type)

	// Construir payload canónico.
	payload := map[string]any{
		"provider": Provider,
		"type":     ev.Type,
		"id":       ev.ID,
		"object":   ev.Data.Object.Object,
		"created":  ev.Created,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     eventType,
		Payload:  payload,
		Raw:      body,
	}, nil
}

// mapStripeEventType traduce tipos de evento de Stripe al evento canónico.
func mapStripeEventType(t string) string {
	switch t {
	case "payment_intent.succeeded":
		return "payment.captured"
	case "payment_intent.payment_failed":
		return "payment.failed"
	case "payment_intent.canceled":
		return "payment.voided"
	case "payment_intent.requires_action", "payment_intent.processing":
		return "payment.pending"
	case "payment_intent.amount_capturable_updated":
		return "payment.authorized"
	case "charge.refunded":
		return "refund.completed"
	case "charge.refund.updated":
		return "refund.completed"
	case "charge.dispute.created":
		return "dispute.opened"
	case "charge.dispute.closed", "charge.dispute.funds_withdrawn":
		return "dispute.resolved"
	default:
		return t
	}
}

// stripeWebhookEvent es el envelope de un evento de Stripe.
type stripeWebhookEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object stripeWebhookPI `json:"object"`
	} `json:"data"`
}

// stripeWebhookPI es el PaymentIntent dentro del evento (subset).
type stripeWebhookPI struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
	Object      string            `json:"object"`
}
