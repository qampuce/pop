package mercadopago

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

// mpVerifier valida la firma HMAC-SHA256 de los webhooks de Mercado Pago.
//
// Header x-signature con formato:
//
//	ts=1614556800,v1=abc123...
//
// La firma v1 es HMAC-SHA256(webhook_secret, "ts:<ts>:data.id:<dataId>")
// donde dataId es el ID del recurso notificado (extraído del query param
// data.id o del body). El secreto se inyecta vía X-MP-Webhook-Secret (el
// HTTP server del SaaS lo conoce por la URL del webhook).
//
// El tenantID se obtiene del header X-MP-Tenant (inyectado por el SaaS).
type mpVerifier struct{}

func (v *mpVerifier) Verify(ctx context.Context, h http.Header, body []byte) (string, error) {
	sigHeader := h.Get("X-Signature")
	if sigHeader == "" {
		// Algunas integraciones viejas usan x-signature en lowercase.
		sigHeader = h.Get("x-signature")
	}
	if sigHeader == "" {
		return "", fmt.Errorf("pop[mercadopago]: missing X-Signature header")
	}

	secret := h.Get("X-MP-Webhook-Secret")
	if secret == "" {
		return "", fmt.Errorf("pop[mercadopago]: missing X-MP-Webhook-Secret header (inject from SaaS HTTP server)")
	}

	ts, sigs, err := parseMPSignature(sigHeader)
	if err != nil {
		return "", err
	}

	// Tolerancia de replay (5 min).
	if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return "", fmt.Errorf("pop[mercadopago]: signature timestamp out of tolerance")
	}

	// data.id: viene en el query param ?data.id=... o en el body como
	// resource o data.id. MP firma "ts:<ts>:data.id:<dataId>".
	dataID := h.Get("X-MP-Data-ID")
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
		return "", fmt.Errorf("pop[mercadopago]: signature mismatch")
	}

	tenantID := strings.TrimSpace(h.Get("X-MP-Tenant"))
	return tenantID, nil
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

// mpNormalizer traduce el payload del evento de Mercado Pago al Event canónico.
//
// MP envía webhooks con formato:
//
//	{"action": "payment.updated", "api_version": "v1", "data": {"id": "123"},
//	 "date_created": "...", "id": <event_id>, "type": "payment", "user_id": <integrator_id>}
//
// El payload NO incluye el payment completo: el SaaS debe hacer un GET
// /v1/payments/:id para obtener el estado. Para mantener el contrato del SDK
// (Normalize devuelve un Event con status/amount), este normalizer hace un
// best-effort con los campos disponibles y deja status=pending si falta info.
// El caller puede hacer el GET posterior y re-normalizar si necesita detalle.
type mpNormalizer struct{}

func (n *mpNormalizer) Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*webhook.Event, error) {
	var ev mpWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("pop[mercadopago]: parse event: %w", err)
	}

	paymentID := ev.Data.ID
	if paymentID == "" {
		paymentID = extractDataID(body)
	}

	out := &webhook.Event{
		ID:              fmt.Sprintf("mercadopago|%s", strconv.FormatInt(ev.ID, 10)),
		Type:            mapMPAction(ev.Action),
		Provider:        Provider,
		TenantID:        tctx.TenantID,
		PaymentID:       paymentID,
		ProviderEventID: strconv.FormatInt(ev.ID, 10),
		CreatedAt:       parseMPDate(ev.DateCreated),
		Raw:             append([]byte(nil), body...),
	}
	// El webhook de MP no incluye status/amount; el caller debe hacer GET.
	out.Status = core.StatusPending
	return out, nil
}

// mapMPAction traduce actions de MP al enum canónico.
func mapMPAction(action string) webhook.EventType {
	switch action {
	case "payment.created", "payment.updated":
		// MP no distingue authorized/captured en el action; el caller debe
		// hacer GET para resolver el status real. Mapeamos a pending como
		// señal de "necesita resolución".
		return webhook.EventPaymentPending
	case "payment.approved":
		return webhook.EventPaymentCaptured
	case "payment.rejected":
		return webhook.EventPaymentFailed
	case "payment.cancelled":
		return webhook.EventPaymentVoided
	case "payment.refunded":
		return webhook.EventRefundCompleted
	case "payment.partial_refunded":
		return webhook.EventRefundCompleted
	case "payment.authorized":
		return webhook.EventPaymentAuthorized
	case "payment.in_process":
		return webhook.EventPaymentPending
	case "merchant_order.created", "merchant_order.updated":
		return webhook.EventPaymentPending
	case "dispute.created", "dispute.opened":
		return webhook.EventDisputeOpened
	case "dispute.closed", "dispute.resolved":
		return webhook.EventDisputeResolved
	default:
		return webhook.EventType(action)
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
