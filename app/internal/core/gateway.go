// Package core define el contrato central del SDK de orquestación de pagos.
//
// Este paquete es la **única** dependencia que el código de negocio del Tenant
// debe conocer. Las implementaciones específicas de proveedores (Stripe,
// Mercado Pago, Kushki, dLocal, Niubiz, Adyen) viven en internal/adapters y
// satisfacen la interfaz Gateway definida acá.
//
// Principios:
//   - El Core NUNCA toca datos crudos de tarjeta (PAN/CVV). Trabaja
//     exclusivamente con tokens generados en el frontend (PCI-DSS scope
//     reduction).
//   - Todas las operaciones reciben un TenantContext que aísla credenciales,
//     configuración y metadata por tenant_id.
//   - Los DTOs de entrada/salida son un único estándar interno; cada adapter
//     traduce su JSON heterogéneo a/desde estos contratos.
package core

import (
	"context"
)

// Gateway es el contrato que todo adapter de pasarela debe implementar.
//
// Es intencionalmente estricto: ningún adapter puede omitir un método. Si un
// proveedor no soporta una operación (ej. tokenize en APMs), debe devolver
// ErrUnsupportedOperation en lugar de paniquear o silenciar el error.
//
// El adapter se construye **por petición** con un TenantContext (credenciales
// desencriptadas del tenant) via la factory. Los métodos NO reciben tctx
// porque ya está baked-in en el adapter: esto garantiza aislamiento estricto
// multi-tenant y evita pasar credenciales por la pila de llamadas.
//
// Todos los métodos reciben:
//   - ctx context.Context: para cancelación/timeout propagados al HTTP client
//     del proveedor.
//   - un DTO de entrada tipado del paquete core.
//
// Devuelven un DTO de salida tipado o un *NormalizedError que ya tradujo el
// código de error del proveedor a un estándar interno.
type Gateway interface {
	// Identificador estable del proveedor (ej. "stripe", "mercadopago").
	// Se usa para routing, logging y selección de adapter en la factory.
	Provider() ProviderID

	// Países y métodos soportados por este proveedor. El router lo usa para
	// filtrar candidatos antes de invocar al adapter.
	Capabilities() Capabilities

	// Tokenize intercambia datos sensibles tokenizados en el frontend por un
	// token del proveedor (vaulting). El SDK nunca recibe PAN/CVV.
	Tokenize(ctx context.Context, in *TokenizeRequest) (*TokenizeResponse, error)

	// Authorize reserva fondos sin capturarlos (auth-only / pre-auth).
	Authorize(ctx context.Context, in *AuthorizeRequest) (*PaymentResult, error)

	// Capture confirma la captura de fondos previamente autorizados.
	Capture(ctx context.Context, in *CaptureRequest) (*PaymentResult, error)

	// Charge ejecuta autorización + captura en una sola operación (auth+capture).
	Charge(ctx context.Context, in *ChargeRequest) (*PaymentResult, error)

	// Refund devuelve fondos total o parcialmente de un pago ya capturado.
	Refund(ctx context.Context, in *RefundRequest) (*RefundResult, error)

	// Void cancela una autorización pendiente antes de su captura.
	Void(ctx context.Context, in *VoidRequest) (*PaymentResult, error)
}

// ProviderID identifica de forma estable un proveedor de pagos.
// No se usa un string libre para evitar typos en routing/configuración.
type ProviderID string

const (
	ProviderStripe       ProviderID = "stripe"
	ProviderMercadoPago  ProviderID = "mercadopago"
	ProviderKushki       ProviderID = "kushki"
	ProviderDLocal       ProviderID = "dlocal"
	ProviderNiubiz       ProviderID = "niubiz"
	ProviderAdyen        ProviderID = "adyen"
)

// Capabilities describe qué puede hacer un proveedor y en qué geografías.
// El router inteligente lo consulta para elegir el mejor adapter por
// país/moneda/método.
type Capabilities struct {
	// Countries en ISO-3166-1 alpha-2. Vacío = global.
	Countries []string `json:"countries"`
	// Currencies en ISO-4217. Vacío = todas las soportadas por el proveedor.
	Currencies []string `json:"currencies"`
	// Methods soportados (card, pix, spei, pse, pago_efectivo, yape, plin...).
	Methods []PaymentMethod `json:"methods"`
	// SupportsAuthOnly indica si el proveedor soporta authorize/capture split.
	SupportsAuthOnly bool `json:"supports_auth_only"`
	// SupportsRefundPartial indica si soporta reembolsos parciales.
	SupportsRefundPartial bool `json:"supports_refund_partial"`
	// SupportsVaulting indica si el proveedor permite vaulting de tokens.
	SupportsVaulting bool `json:"supports_vaulting"`
}

// PaymentMethod es el identificador estándar interno de un método de pago.
// Normaliza los APMs (Alternative Payment Methods) bajo un único enum.
type PaymentMethod string

const (
	MethodCard        PaymentMethod = "card"
	MethodPix         PaymentMethod = "pix"          // Brasil
	MethodSPEI        PaymentMethod = "spei"         // México
	MethodPSE         PaymentMethod = "pse"          // Colombia
	MethodPagoEfectivo PaymentMethod = "pago_efectivo" // Perú
	MethodYape        PaymentMethod = "yape"         // Perú
	MethodPlin        PaymentMethod = "plin"         // Perú
)
