// Package pop es la API pública del SDK de orquestación de pagos.
//
// Es el único paquete que el código de negocio del Tenant debe importar.
// Expone un Client que combina: factory de adapters, router inteligente,
// cascading con reintentos, vault de tokens y normalización de webhooks.
//
// Uso canónico:
//
//	client := pop.New(pop.Config{
//	    Credentials: myVault,        // core.CredentialResolver
//	    TokenVault:  myTokenVault,   // vault.Vault
//	    RoutingRules: &pop.RoutingRules{...},
//	})
//
//	res, err := client.Charge(ctx, &pop.ChargeRequest{
//	    TenantID: "tnt_123",
//	    Mode:     pop.Live,
//	    Country:  "PE",
//	    Reference: "order_456",
//	    Amount:    pop.Money{Amount: 19990, Currency: "PEN"},
//	    Method:    pop.MethodCard,
//	    ProviderToken: "tok_xxx",
//	})
package pop

import (
	"context"
	"fmt"
	"net/http"

	_ "github.com/qampu/pop/internal/adapters/mock" // registra mock en factory.Default
	_ "github.com/qampu/pop/internal/adapters/stripe" // registra stripe en factory.Default
	_ "github.com/qampu/pop/internal/adapters/mercadopago" // registra mercadopago en factory.Default
	_ "github.com/qampu/pop/internal/adapters/kushki" // registra kushki en factory.Default
	_ "github.com/qampu/pop/internal/adapters/dlocal" // registra dlocal en factory.Default
	_ "github.com/qampu/pop/internal/adapters/niubiz" // registra niubiz en factory.Default
	_ "github.com/qampu/pop/internal/adapters/adyen" // registra adyen en factory.Default
	"github.com/qampu/pop/internal/cascading"
	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/routing"
	"github.com/qampu/pop/internal/vault"
	"github.com/qampu/pop/internal/webhook"
)

// Config del SDK.
type Config struct {
	// Credentials resuelve credenciales por tenant (encriptadas en reposo).
	Credentials core.CredentialResolver
	// TokenVault opcional para guardar tokens de proveedor reutilizables.
	TokenVault vault.Vault
	// RoutingRules del tenant (prioridades, blacklist, fallbacks).
	RoutingRules *routing.RoutingRules
	// CascadePolicy de reintentos. Si nil, usa DefaultPolicy.
	CascadePolicy cascading.Policy
	// Registry de adapters. Si nil, usa factory.Default (poblado por
	// blank imports de cada adapter).
	Registry *factory.Registry
	// WebhookRegistry de handlers. Si nil, usa webhook.Default.
	WebhookRegistry *webhook.Registry
}

// Client es la entrada principal al SDK.
type Client struct {
	cfg       Config
	registry  *factory.Registry
	router    *routing.Router
	cascader  *cascading.Cascader
	webhooks  *webhook.Registry
	tokens    vault.Vault
}

// New construye un Client.
func New(cfg Config) (*Client, error) {
	if cfg.Credentials == nil {
		return nil, fmt.Errorf("pop: Credentials resolver is required")
	}
	reg := cfg.Registry
	if reg == nil {
		reg = factory.Default
	}
	wr := cfg.WebhookRegistry
	if wr == nil {
		wr = webhook.Default
	}
	policy := cfg.CascadePolicy
	if policy.MaxAttempts == 0 {
		policy = cascading.DefaultPolicy
	}
	return &Client{
		cfg:      cfg,
		registry: reg,
		router:   routing.NewRouter(reg),
		cascader: cascading.NewCascader(reg, policy),
		webhooks: wr,
		tokens:   cfg.TokenVault,
	}, nil
}

// --- Re-exports de tipos públicos para ergonomía ---

type (
	Money          = core.Money
	CardToken      = core.CardToken
	Buyer          = core.Buyer
	Address        = core.Address
	Environment    = core.Environment
	PaymentMethod  = core.PaymentMethod
	PaymentStatus  = core.PaymentStatus
	ProviderID     = core.ProviderID
	PaymentResult  = core.PaymentResult
	RefundResult   = core.RefundResult
	TokenizeRequest  = core.TokenizeRequest
	TokenizeResponse = core.TokenizeResponse
	AuthorizeRequest = core.AuthorizeRequest
	CaptureRequest   = core.CaptureRequest
	ChargeRequest    = core.ChargeRequest
	RefundRequest    = core.RefundRequest
	VoidRequest      = core.VoidRequest
	NormalizedError  = core.NormalizedError
	RoutingRules     = routing.RoutingRules
	Event            = webhook.Event
	EventType        = webhook.EventType
	RefundReason     = core.RefundReason
)

// Constantes públicas.
const (
	Test Environment = core.EnvTest
	Live Environment = core.EnvLive

	MethodCard         PaymentMethod = core.MethodCard
	MethodPix          PaymentMethod = core.MethodPix
	MethodSPEI         PaymentMethod = core.MethodSPEI
	MethodPSE          PaymentMethod = core.MethodPSE
	MethodPagoEfectivo PaymentMethod = core.MethodPagoEfectivo
	MethodYape         PaymentMethod = core.MethodYape
	MethodPlin         PaymentMethod = core.MethodPlin

	ProviderStripe      ProviderID = core.ProviderStripe
	ProviderMercadoPago ProviderID = core.ProviderMercadoPago
	ProviderKushki      ProviderID = core.ProviderKushki
	ProviderDLocal      ProviderID = core.ProviderDLocal
	ProviderNiubiz      ProviderID = core.ProviderNiubiz
	ProviderAdyen       ProviderID = core.ProviderAdyen

	StatusPending           PaymentStatus = core.StatusPending
	StatusAuthorized        PaymentStatus = core.StatusAuthorized
	StatusCaptured          PaymentStatus = core.StatusCaptured
	StatusFailed            PaymentStatus = core.StatusFailed
	StatusVoided            PaymentStatus = core.StatusVoided
	StatusRefunded          PaymentStatus = core.StatusRefunded
	StatusPartiallyRefunded PaymentStatus = core.StatusPartiallyRefunded

	RefundDuplicate           RefundReason = core.RefundDuplicate
	RefundFraudulent          RefundReason = core.RefundFraudulent
	RefundRequestedByCustomer RefundReason = core.RefundRequestedByCustomer
	RefundProductNotReceived  RefundReason = core.RefundProductNotReceived
)

// Eventos de webhook canónicos (re-export de webhook.EventType).
const (
	EventPaymentAuthorized EventType = webhook.EventPaymentAuthorized
	EventPaymentCaptured   EventType = webhook.EventPaymentCaptured
	EventPaymentFailed     EventType = webhook.EventPaymentFailed
	EventPaymentVoided     EventType = webhook.EventPaymentVoided
	EventPaymentPending    EventType = webhook.EventPaymentPending
	EventRefundCreated     EventType = webhook.EventRefundCreated
	EventRefundCompleted   EventType = webhook.EventRefundCompleted
	EventRefundFailed      EventType = webhook.EventRefundFailed
	EventDisputeOpened     EventType = webhook.EventDisputeOpened
	EventDisputeResolved   EventType = webhook.EventDisputeResolved
)

// --- Operaciones ---

// ChargeRequestExt extiende ChargeRequest con el contexto del tenant.
// El SDK necesita tenantID + mode + country para resolver credenciales y
// rutear. Estos campos no viven en el DTO core (que es puro de dominio).
type ChargeRequestExt struct {
	ChargeRequest
	TenantID string      `json:"tenant_id"`
	Mode     Environment `json:"mode"`
	Country  string      `json:"country"`
}

// Charge ejecuta auth+capture con routing + cascading.
func (c *Client) Charge(ctx context.Context, req *ChargeRequestExt) (*PaymentResult, error) {
	providers, err := c.router.Route(ctx, &routing.RouteRequest{
		Country:  req.Country,
		Currency: req.Amount.Currency,
		Method:   req.Method,
		Amount:   req.Amount,
		Rules:    c.cfg.RoutingRules,
	})
	if err != nil {
		return nil, err
	}
	return cascading.Run(ctx, c.cascader, c.cfg.Credentials, req.TenantID, req.Mode, providers,
		func(ctx context.Context, gw core.Gateway) (*PaymentResult, error) {
			return gw.Charge(ctx, &req.ChargeRequest)
		})
}

// Tokenize delega al adapter del proveedor indicado.
func (c *Client) Tokenize(ctx context.Context, tenantID string, provider ProviderID, mode Environment, in *TokenizeRequest) (*TokenizeResponse, error) {
	gw, err := c.registry.BuildFromCredentials(ctx, c.cfg.Credentials, tenantID, provider, mode)
	if err != nil {
		return nil, err
	}
	return gw.Tokenize(ctx, in)
}

// AuthorizeRequestExt extiende AuthorizeRequest con contexto del tenant.
type AuthorizeRequestExt struct {
	AuthorizeRequest
	TenantID string      `json:"tenant_id"`
	Mode     Environment `json:"mode"`
	Country  string      `json:"country"`
}

// Authorize ejecuta auth-only con routing + cascading.
func (c *Client) Authorize(ctx context.Context, req *AuthorizeRequestExt) (*PaymentResult, error) {
	providers, err := c.router.Route(ctx, &routing.RouteRequest{
		Country:  req.Country,
		Currency: req.Amount.Currency,
		Method:   req.Method,
		Amount:   req.Amount,
		Rules:    c.cfg.RoutingRules,
	})
	if err != nil {
		return nil, err
	}
	return cascading.Run(ctx, c.cascader, c.cfg.Credentials, req.TenantID, req.Mode, providers,
		func(ctx context.Context, gw core.Gateway) (*PaymentResult, error) {
			return gw.Authorize(ctx, &req.AuthorizeRequest)
		})
}

// CaptureRequestExt extiende CaptureRequest con contexto del tenant.
// Capture opera contra el proveedor que procesó la auth original, por eso
// recibe Provider explícito (sin routing).
type CaptureRequestExt struct {
	CaptureRequest
	TenantID string      `json:"tenant_id"`
	Mode     Environment `json:"mode"`
	Provider ProviderID   `json:"provider"`
}

// Capture confirma fondos previamente autorizados.
func (c *Client) Capture(ctx context.Context, req *CaptureRequestExt) (*PaymentResult, error) {
	return cascading.Run(ctx, c.cascader, c.cfg.Credentials, req.TenantID, req.Mode,
		[]core.ProviderID{req.Provider},
		func(ctx context.Context, gw core.Gateway) (*PaymentResult, error) {
			return gw.Capture(ctx, &req.CaptureRequest)
		})
}

// RefundRequestExt extiende RefundRequest con contexto del tenant.
type RefundRequestExt struct {
	RefundRequest
	TenantID string      `json:"tenant_id"`
	Mode     Environment `json:"mode"`
	Provider ProviderID   `json:"provider"`
}

// Refund devuelve fondos total o parcialmente.
func (c *Client) Refund(ctx context.Context, req *RefundRequestExt) (*RefundResult, error) {
	return cascading.Run(ctx, c.cascader, c.cfg.Credentials, req.TenantID, req.Mode,
		[]core.ProviderID{req.Provider},
		func(ctx context.Context, gw core.Gateway) (*RefundResult, error) {
			return gw.Refund(ctx, &req.RefundRequest)
		})
}

// VoidRequestExt extiende VoidRequest con contexto del tenant.
type VoidRequestExt struct {
	VoidRequest
	TenantID string      `json:"tenant_id"`
	Mode     Environment `json:"mode"`
	Provider ProviderID   `json:"provider"`
}

// Void cancela una autorización pendiente.
func (c *Client) Void(ctx context.Context, req *VoidRequestExt) (*PaymentResult, error) {
	return cascading.Run(ctx, c.cascader, c.cfg.Credentials, req.TenantID, req.Mode,
		[]core.ProviderID{req.Provider},
		func(ctx context.Context, gw core.Gateway) (*PaymentResult, error) {
			return gw.Void(ctx, &req.VoidRequest)
		})
}

// ProcessWebhook normaliza un webhook entrante.
//
// Delega al webhook.Registry: verifica la firma del proveedor, resuelve el
// TenantContext a partir del tenant identificado en la firma, y normaliza
// el payload crudo a un Event estándar.
//
// El caller típicamente extrae el provider de la URL path (ej. /webhooks/stripe)
// en su HTTP server y le pasa el *http.Request tal cual.
func (c *Client) ProcessWebhook(ctx context.Context, provider ProviderID, mode Environment, r *http.Request) (*webhook.Event, error) {
	return c.webhooks.Process(ctx, provider, c.cfg.Credentials, mode, r)
}
