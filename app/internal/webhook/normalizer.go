// Package webhook implementa el normalizador de webhooks unificado.
//
// Cada proveedor envía webhooks con schemas distintos, headers de firma
// distintos y mecanismos de validación distintos. Este paquete traduce
// cualquier webhook entrante a un Evento estándar interno que el código
// de negocio del Tenant consume de forma uniforme.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

// EventType es el enum canónico de eventos de webhook.
// Normaliza los eventos heterogéneos de los proveedores.
type EventType string

const (
	EventPaymentAuthorized EventType = "payment.authorized"
	EventPaymentCaptured   EventType = "payment.captured"
	EventPaymentFailed     EventType = "payment.failed"
	EventPaymentVoided     EventType = "payment.voided"
	EventPaymentPending    EventType = "payment.pending"
	EventRefundCreated     EventType = "refund.created"
	EventRefundCompleted   EventType = "refund.completed"
	EventRefundFailed      EventType = "refund.failed"
	EventDisputeOpened     EventType = "dispute.opened"
	EventDisputeResolved   EventType = "dispute.resolved"
)

// Event es el evento normalizado que el código de negocio recibe.
type Event struct {
	// ID interno del evento (dedup). Combinación provider+provider_event_id.
	ID string `json:"id"`
	// Type canónico del evento.
	Type EventType `json:"type"`
	// Provider que originó el evento.
	Provider core.ProviderID `json:"provider"`
	// TenantID destinatario (resuelto al validar la firma).
	TenantID string `json:"tenant_id"`
	// PaymentID del pago referenciado (normalizado).
	PaymentID string `json:"payment_id"`
	// Reference del tenant.
	Reference string `json:"reference"`
	// Status normalizado del pago post-evento.
	Status core.PaymentStatus `json:"status"`
	// Amount del pago.
	Amount core.Money `json:"amount"`
	// ProviderEventID crudo para dedup/idempotencia.
	ProviderEventID string `json:"provider_event_id"`
	// CreatedAt del evento en el proveedor.
	CreatedAt time.Time `json:"created_at"`
	// Raw payload crudo del proveedor (para debugging/auditoría).
	Raw json.RawMessage `json:"raw"`
}

// Verifier valida la firma/autenticidad de un webhook entrante.
// Cada adapter implementa el suyo (HMAC SHA256, header signature, etc.).
type Verifier interface {
	// Verify devuelve el TenantID si la firma es válida, o error.
	// El TenantID se obtiene típicamente del header/signature mapeado a
	// la configuración del tenant (webhook secret).
	Verify(ctx context.Context, headers http.Header, body []byte) (tenantID string, err error)
}

// Normalizer traduce el payload crudo verificado a un Event estándar.
type Normalizer interface {
	Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*Event, error)
}

// WebhookHandler combina verificación + normalización para un proveedor.
type WebhookHandler struct {
	Provider  core.ProviderID
	Verifier  Verifier
	Normalize Normalizer
}

// Registry de handlers de webhook por proveedor.
type Registry struct {
	handlers map[core.ProviderID]*WebhookHandler
}

// NewRegistry crea un registry vacío.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[core.ProviderID]*WebhookHandler)}
}

// Register un handler para un proveedor.
func (r *Registry) Register(h *WebhookHandler) {
	if h == nil || h.Provider == "" {
		return
	}
	r.handlers[h.Provider] = h
}

// Process procesa una request HTTP entrante de webhook.
//
// Flujo:
//  1. Detectar el proveedor (por path o header, ej. /webhooks/stripe).
//  2. Verificar firma → obtener tenantID.
//  3. Resolver credenciales del tenant (para webhook secret ya validado).
//  4. Normalizar payload a Event.
//  5. Devolver Event al caller para que lo despache al bus de eventos.
//
// El provider se pasa explícitamente (típicamente extraído de la URL path
// en el HTTP server del SaaS).
func (r *Registry) Process(
	ctx context.Context,
	provider core.ProviderID,
	resolver core.CredentialResolver,
	mode core.Environment,
	req *http.Request,
) (*Event, error) {
	h, ok := r.handlers[provider]
	if !ok {
		return nil, fmt.Errorf("pop: no webhook handler for provider %s", provider)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("pop: read body: %w", err)
	}
	defer req.Body.Close()

	tenantID, err := h.Verifier.Verify(ctx, req.Header, body)
	if err != nil {
		return nil, core.NewError(core.ErrWebhookSignature, core.CategoryAuth, provider,
			fmt.Sprintf("webhook signature invalid: %v", err))
	}

	tctx, err := resolver.Resolve(ctx, tenantID, provider, mode)
	if err != nil {
		return nil, fmt.Errorf("pop: resolve tenant for webhook: %w", err)
	}

	ev, err := h.Normalize.Normalize(ctx, tctx, body)
	if err != nil {
		return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, provider,
			fmt.Sprintf("webhook parse error: %v", err))
	}
	ev.Provider = provider
	ev.TenantID = tenantID
	return ev, nil
}

// Default registry global (poblado por blank imports de adapters).
var Default = NewRegistry()

// DefaultRegistry alias para compatibilidad con factory.Default.
var DefaultRegistry = Default

// Ensure factory.Default is referenced (los adapters lo poblarán).
var _ = factory.Default
