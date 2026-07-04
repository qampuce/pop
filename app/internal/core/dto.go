package core

import (
	"time"
)

// Money representa un monto monetario tipado. Siempre en la unidad más pequeña
// del país (cents para USD/EUR, centavos para BRL/MXN, etc.) para evitar
// pérdida de precisión con floats. El adapter traduce a la unidad esperada
// por el proveedor.
type Money struct {
	// Amount en la unidad mínima (ej. 1999 = $19.99 USD).
	Amount int64 `json:"amount"`
	// Currency ISO-4217 (ej. "USD", "PEN", "BRL").
	Currency string `json:"currency"`
}

// CardToken es el resultado de tokenizar en el frontend. El SDK NUNCA recibe
// PAN/CVV: solo este token opaco + datos no sensibles del portador.
type CardToken struct {
	// Token generado por el frontend del proveedor (PaymentIntent client
	// secret, Kushki token, etc.). Opaco para el Core.
	Token string `json:"token"`
	// Brand detectada por el frontend (visa, mastercard, amex...). Informativa.
	Brand string `json:"brand,omitempty"`
	// Last4 del PAN (PCI-DSS permite guardar hasta 6+4 dígitos).
	Last4 string `json:"last4,omitempty"`
	// Bin6 primeros dígitos (para routing/bin-range).
	Bin6 string `json:"bin6,omitempty"`
	// ExpMonth/ExpYear visibles (no sensibles bajo PCI-DSS).
	ExpMonth int `json:"exp_month,omitempty"`
	ExpYear  int `json:"exp_year,omitempty"`
	// HolderName nombre del portador (no sensible).
	HolderName string `json:"holder_name,omitempty"`
}

// Buyer describe al comprador. Datos PII manejados con cuidado: el adapter
// solo envía al proveedor los campos que este requiere.
type Buyer struct {
	ID        string `json:"id,omitempty"`
	Email     string `json:"email,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
	// Country ISO-3166-1 alpha-2.
	Country string `json:"country,omitempty"`
	// DocType/DocNumber documento de identidad (ej. DNI, CPF, CURP).
	// Requerido por varios APMs latam (Pix, PSE, PagoEfectivo).
	DocType   string `json:"doc_type,omitempty"`
	DocNumber string `json:"doc_number,omitempty"`
	// IP del comprador (anti-fraude / geolocalización).
	IP string `json:"ip,omitempty"`
}

// Address normalizado para billing/shipping.
type Address struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// TokenizeRequest — intercambia datos tokenizados del frontend por un token
// del proveedor (vaulting). El SDK no recibe PAN/CVV.
type TokenizeRequest struct {
	// Method que se está tokenizando (card, pix, spei...).
	Method PaymentMethod `json:"method"`
	// CardToken si Method == "card". Nulo para APMs.
	Card *CardToken `json:"card,omitempty"`
	// APMData datos específicos del APM (ej. CPF para Pix, bank code para PSE).
	// Estructura libre: cada adapter sabe qué campos espera su proveedor.
	APMData map[string]string `json:"apm_data,omitempty"`
	// Buyer del portador (algunos proveedores lo requieren al tokenizar).
	Buyer *Buyer `json:"buyer,omitempty"`
	// Metadata libre del tenant para trazabilidad.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// TokenizeResponse — token del proveedor listo para usar en authorize/charge.
type TokenizeResponse struct {
	// ProviderToken token opaco del proveedor (ej. pm_token de Stripe).
	ProviderToken string `json:"provider_token"`
	// Vaulted true si el token quedó guardado en la bóveda del proveedor
	// (reutilizable para cargos recurrentes).
	Vaulted bool `json:"vaulted"`
	// Method confirmado por el proveedor.
	Method PaymentMethod `json:"method"`
	// Last4/Brand reflejados (si aplica).
	Last4 string `json:"last4,omitempty"`
	Brand string `json:"brand,omitempty"`
	// ExpiresAt opcional del token.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Raw respuesta cruda del proveedor (para debugging, NUNCA con PAN).
	Raw map[string]any `json:"raw,omitempty"`
}

// AuthorizeRequest — reserva fondos sin capturar (auth-only).
type AuthorizeRequest struct {
	// Reference ID interno del tenant (order_id, cart_id). Se propaga como
	// idempotency ref y como referencia en el dashboard del proveedor.
	Reference string `json:"reference"`
	// Amount a autorizar.
	Amount Money `json:"amount"`
	// Method de pago.
	Method PaymentMethod `json:"method"`
	// ProviderToken obtenido vía Tokenize (card) o datos APM.
	ProviderToken string `json:"provider_token,omitempty"`
	// Buyer del comprador.
	Buyer *Buyer `json:"buyer,omitempty"`
	// BillingAddress del portador.
	BillingAddress *Address `json:"billing_address,omitempty"`
	// ShippingAddress opcional (anti-fraude).
	ShippingAddress *Address `json:"shipping_address,omitempty"`
	// Description legible del cargo.
	Description string `json:"description,omitempty"`
	// Metadata libre del tenant.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// CaptureRequest — captura fondos previamente autorizados.
type CaptureRequest struct {
	// AuthorizationID devuelto por Authorize.
	AuthorizationID string `json:"authorization_id"`
	// Amount a capturar. Puede ser menor que el autorizado (captura parcial)
	// si el proveedor lo soporta (Capabilities.SupportsAuthOnly).
	Amount Money `json:"amount"`
	// Metadata libre.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ChargeRequest — autorización + captura en una sola operación.
// Es el flujo más común para pagos directos sin pre-auth.
type ChargeRequest struct {
	Reference       string            `json:"reference"`
	Amount          Money             `json:"amount"`
	Method          PaymentMethod     `json:"method"`
	ProviderToken   string            `json:"provider_token,omitempty"`
	Buyer           *Buyer            `json:"buyer,omitempty"`
	BillingAddress  *Address          `json:"billing_address,omitempty"`
	ShippingAddress *Address          `json:"shipping_address,omitempty"`
	Description     string            `json:"description,omitempty"`
	// Capture false = auth-only equivalente a Authorize. Default true.
	Capture  bool              `json:"capture"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RefundRequest — devuelve fondos total o parcialmente.
type RefundRequest struct {
	// PaymentID del cargo original a reembolsar.
	PaymentID string `json:"payment_id"`
	// Amount a reembolsar. Si es 0 y el proveedor lo soporta, reembolso total.
	Amount Money `json:"amount"`
	// Reason normalizado (duplicated, fraudulent, requested_by_customer...).
	Reason RefundReason `json:"reason"`
	// Metadata libre.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RefundReason estandariza motivos de reembolso para reportes multi-proveedor.
type RefundReason string

const (
	RefundDuplicate          RefundReason = "duplicate"
	RefundFraudulent         RefundReason = "fraudulent"
	RefundRequestedByCustomer RefundReason = "requested_by_customer"
	RefundProductNotReceived RefundReason = "product_not_received"
)

// VoidRequest — cancela una autorización pendiente antes de captura.
type VoidRequest struct {
	AuthorizationID string            `json:"authorization_id"`
	Reason          string            `json:"reason,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// PaymentResult — DTO de salida estándar para authorize/capture/charge/void.
// Unifica el esquema heterogéneo de todos los proveedores.
type PaymentResult struct {
	// ID interno del pago en el proveedor.
	ID string `json:"id"`
	// AuthorizationID si la operación generó una auth separada.
	AuthorizationID string `json:"authorization_id,omitempty"`
	// Status normalizado del pago.
	Status PaymentStatus `json:"status"`
	// Method confirmado.
	Method PaymentMethod `json:"method"`
	// Amount procesado (puede diferir del solicitado en capturas parciales).
	Amount Money `json:"amount"`
	// Provider que procesó la operación.
	Provider ProviderID `json:"provider"`
	// Country de procesamiento.
	Country string `json:"country"`
	// TenantID dueño del pago.
	TenantID string `json:"tenant_id"`
	// Reference del tenant.
	Reference string `json:"reference"`
	// CreatedAt en el proveedor (UTC).
	CreatedAt time.Time `json:"created_at"`
	// NextAction para flujos que requieren acción del cliente (3DS, redirect
	// APM, Pix QR code, etc.). El frontend lo consume para continuar.
	NextAction *NextAction `json:"next_action,omitempty"`
	// Raw respuesta cruda del proveedor (debugging).
	Raw map[string]any `json:"raw,omitempty"`
}

// PaymentStatus normaliza el estado del pago a un único enum interno.
// Cada adapter mapea sus estados propios a estos valores.
type PaymentStatus string

const (
	StatusPending         PaymentStatus = "pending"          // requiere acción / en proceso
	StatusAuthorized      PaymentStatus = "authorized"       // fondos reservados, sin capturar
	StatusCaptured        PaymentStatus = "captured"         // capturado / completado
	StatusFailed          PaymentStatus = "failed"           // falló definitivamente
	StatusVoided          PaymentStatus = "voided"           // autorización cancelada
	StatusRefunded        PaymentStatus = "refunded"         // reembolsado total
	StatusPartiallyRefunded PaymentStatus = "partially_refunded"
)

// NextAction describe qué debe hacer el frontend para continuar el pago.
// Cubre 3DS, redirect APM, QR code (Pix), app switch (Yape/Plin), etc.
type NextAction struct {
	Type NextActionType `json:"type"`
	// RedirectURL para flujos redirect (PSE, PagoEfectivo, 3DS redirect).
	RedirectURL string `json:"redirect_url,omitempty"`
	// QRCode base64 o payload para Pix.
	QRCode string `json:"qr_code,omitempty"`
	// QRPayload string cruda del QR (Pix "copia e cola").
	QRPayload string `json:"qr_payload,omitempty"`
	// DeepLink para app switch (Yape, Plin).
	DeepLink string `json:"deep_link,omitempty"`
	// Token3DS para challenge 3DS embebido.
	Token3DS string `json:"token_3ds,omitempty"`
	// ExpiresAt de la acción (ej. Pix expira en 30 min).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type NextActionType string

const (
	NextActionRedirect  NextActionType = "redirect"
	NextActionQR        NextActionType = "qr"
	NextActionAppSwitch NextActionType = "app_switch"
	NextAction3DS       NextActionType = "3ds"
	NextActionWait      NextActionType = "wait"
)

// RefundResult — DTO de salida para refunds.
type RefundResult struct {
	ID             string        `json:"id"`
	PaymentID      string        `json:"payment_id"`
	Status         PaymentStatus `json:"status"`
	Amount         Money         `json:"amount"`
	Provider       ProviderID    `json:"provider"`
	TenantID       string        `json:"tenant_id"`
	Reference      string        `json:"reference"`
	CreatedAt      time.Time     `json:"created_at"`
	Raw            map[string]any `json:"raw,omitempty"`
}
