package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// stripeVerifier valida la firma HMAC-SHA256 de los webhooks de Stripe.
//
// Header Stripe-Signature con formato:
//
//	t=1614556800,v1=abc123...,v0=def456...
//
// La firma v1 es HMAC-SHA256(webhook_secret, "${t}.${body}"). Se compara en
// tiempo constante. El tenantID se obtiene del campo Account del evento (si
// está disponible) o se deja vacío para que el caller lo resuelva por contexto
// (típicamente el path de la URL del webhook incluye el tenant).
//
// Como el Verifier se ejecuta ANTES de resolver el TenantContext, no tenemos
// acceso al webhook_secret del tenant en este punto. Por eso el Verifier
// acepta el secreto vía el header X-Stripe-Webhook-Secret (inyectado por el
// HTTP server del SaaS que sí conoce el tenant por la URL) o, en su defecto,
// confía en el secreto resuelto después. Para mantener el contrato del SDK
// (Verify devuelve tenantID), usamos el header X-Stripe-Tenant si está
// presente; si no, devolvemos un tenantID placeholder que el caller puede
// reasignar.
type stripeVerifier struct{}

func (v *stripeVerifier) Verify(ctx context.Context, h http.Header, body []byte) (string, error) {
	sigHeader := h.Get("Stripe-Signature")
	if sigHeader == "" {
		return "", fmt.Errorf("pop[stripe]: missing Stripe-Signature header")
	}

	secret := h.Get("X-Stripe-Webhook-Secret")
	if secret == "" {
		return "", fmt.Errorf("pop[stripe]: missing X-Stripe-Webhook-Secret header (inject from SaaS HTTP server)")
	}

	ts, sigs, err := parseSignatureHeader(sigHeader)
	if err != nil {
		return "", err
	}

	// Tolerancia de replay (5 min).
	if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return "", fmt.Errorf("pop[stripe]: signature timestamp out of tolerance")
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
		return "", fmt.Errorf("pop[stripe]: signature mismatch")
	}

	tenantID := strings.TrimSpace(h.Get("X-Stripe-Tenant"))
	return tenantID, nil
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

// stripeNormalizer traduce el payload del evento de Stripe al Event canónico.
type stripeNormalizer struct{}

func (n *stripeNormalizer) Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*webhook.Event, error) {
	var ev stripeWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("pop[stripe]: parse event: %w", err)
	}

	pi := ev.Data.Object
	out := &webhook.Event{
		ID:              fmt.Sprintf("stripe|%s", ev.ID),
		Type:            mapEventType(ev.Type),
		Provider:        Provider,
		TenantID:        tctx.TenantID,
		PaymentID:       pi.ID,
		ProviderEventID: ev.ID,
		CreatedAt:       time.Unix(ev.Created, 0).UTC(),
		Raw:             append([]byte(nil), body...),
	}
	out.Status = mapPIStatus(pi.Status)
	out.Amount = core.Money{Amount: pi.Amount, Currency: strings.ToUpper(pi.Currency)}
	out.Reference = pi.Description
	if out.Reference == "" {
		// Stripe no siempre propaga el reference; intentamos metadata.
		if ref, ok := pi.Metadata["reference"]; ok {
			out.Reference = ref
		}
	}
	return out, nil
}

// mapEventType traduce tipos de evento de Stripe al enum canónico.
func mapEventType(t string) webhook.EventType {
	switch t {
	case "payment_intent.succeeded":
		return webhook.EventPaymentCaptured
	case "payment_intent.payment_failed":
		return webhook.EventPaymentFailed
	case "payment_intent.canceled":
		return webhook.EventPaymentVoided
	case "payment_intent.requires_action", "payment_intent.processing":
		return webhook.EventPaymentPending
	case "payment_intent.amount_capturable_updated":
		return webhook.EventPaymentAuthorized
	case "charge.refunded":
		return webhook.EventRefundCompleted
	case "charge.refund.updated":
		return webhook.EventRefundCompleted
	case "charge.dispute.created":
		return webhook.EventDisputeOpened
	case "charge.dispute.closed", "charge.dispute.funds_withdrawn":
		return webhook.EventDisputeResolved
	default:
		return webhook.EventType(t) // passthrough para eventos no mapeados
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
}
