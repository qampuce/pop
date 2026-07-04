package core

import (
	"errors"
	"fmt"
)

// NormalizedError es el error estándar del SDK. Cada adapter traduce los
// códigos de error heterogéneos del proveedor a un ErrorCode canónico.
//
// Esto permite que el código de negocio del Tenant reaccione de forma
// uniforme sin conocer los detalles de cada pasarela (ej. "card_declined"
// en Stripe vs "cc_rejected_card_declined" en Mercado Pago se mapean a
// ErrCardDeclined).
//
// Implementa la interfaz error y unwrap, para que errors.Is/As funcionen.
type NormalizedError struct {
	// Code canónico interno (ver ErrorCode constants).
	Code ErrorCode `json:"code"`
	// Category del error (network, gateway, validation, fraud...).
	Category ErrorCategory `json:"category"`
	// Message legible para logs/dashboard (NO para el usuario final).
	Message string `json:"message"`
	// Provider que generó el error original.
	Provider ProviderID `json:"provider"`
	// ProviderCode código crudo del proveedor (para soporte/debug).
	ProviderCode string `json:"provider_code,omitempty"`
	// ProviderMessage mensaje crudo del proveedor.
	ProviderMessage string `json:"provider_message,omitempty"`
	// Retryable true si el error es transitorio y el cascading puede
	// reintentar con otro proveedor (timeout, 5xx, rate limit).
	Retryable bool `json:"retryable"`
	// DeclineCode específico de rechazos de tarjeta (insufficient_funds,
	// expired_card, do_not_honor...). Vacío si no aplica.
	DeclineCode string `json:"decline_code,omitempty"`
	// Cause error original para unwrap (no se serializa).
	cause error `json:"-"`
}

// Error implementa error.
func (e *NormalizedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.ProviderCode != "" {
		return fmt.Sprintf("pop[%s]: %s (provider=%s code=%s): %s",
			e.Code, e.Message, e.Provider, e.ProviderCode, e.ProviderMessage)
	}
	return fmt.Sprintf("pop[%s]: %s (provider=%s)", e.Code, e.Message, e.Provider)
}

// Unwrap permite errors.Is/As.
func (e *NormalizedError) Unwrap() error { return e.cause }

// Wrap envuelve un error original manteniendo el contexto normalizado.
func (e *NormalizedError) Wrap(cause error) *NormalizedError {
	ne := *e
	ne.cause = cause
	return &ne
}

// ErrorCategory agrupa errores para decisión de routing/cascading.
type ErrorCategory string

const (
	CategoryValidation  ErrorCategory = "validation"   // input inválido del tenant
	CategoryNetwork     ErrorCategory = "network"      // timeout, DNS, conexión
	CategoryGateway     ErrorCategory = "gateway"      // 4xx/5xx del proveedor
	CategoryDecline     ErrorCategory = "decline"      // tarjeta rechazada
	CategoryFraud       ErrorCategory = "fraud"        // bloqueo anti-fraude
	CategoryAuth        ErrorCategory = "auth"         // credenciales inválidas
	CategoryRateLimit   ErrorCategory = "rate_limit"   // throttling
	CategoryUnsupported ErrorCategory = "unsupported"  // operación no soportada
	CategoryInternal    ErrorCategory = "internal"     // bug del SDK/adapter
)

// ErrorCode es el código canónico de error. Estable y versionado: el código
// de negocio del Tenant puede switch sobre estos valores con seguridad.
type ErrorCode string

const (
	// Errores de input / configuración.
	ErrInvalidRequest       ErrorCode = "invalid_request"
	ErrMissingCredentials   ErrorCode = "missing_credentials"
	ErrUnsupportedOperation ErrorCode = "unsupported_operation"
	ErrUnsupportedMethod    ErrorCode = "unsupported_method"
	ErrUnsupportedCountry   ErrorCode = "unsupported_country"
	ErrUnsupportedCurrency  ErrorCode = "unsupported_currency"

	// Errores transitorios (retryable).
	ErrTimeout        ErrorCode = "timeout"
	ErrRateLimited    ErrorCode = "rate_limited"
	ErrProviderDown   ErrorCode = "provider_down"
	ErrNetworkError   ErrorCode = "network_error"

	// Errores de autenticación del tenant contra el proveedor.
	ErrInvalidCredentials ErrorCode = "invalid_credentials"
	ErrUnauthorized       ErrorCode = "unauthorized"

	// Rechazos de tarjeta (declines). Mapeados desde el código crudo del
	// proveedor a este estándar.
	ErrCardDeclined       ErrorCode = "card_declined"
	ErrInsufficientFunds  ErrorCode = "insufficient_funds"
	ErrExpiredCard        ErrorCode = "expired_card"
	ErrInvalidCVC         ErrorCode = "invalid_cvc"
	ErrInvalidNumber      ErrorCode = "invalid_card_number"
	ErrSuspectedFraud     ErrorCode = "suspected_fraud"
	ErrDoNotHonor         ErrorCode = "do_not_honor"
	ErrLostCard           ErrorCode = "lost_card"
	ErrStolenCard         ErrorCode = "stolen_card"
	ErrLimitExceeded      ErrorCode = "limit_exceeded"
	ErrProcessingError    ErrorCode = "processing_error"

	// Errores de APMs.
	ErrAPMRejected        ErrorCode = "apm_rejected"
	ErrAPMExpired         ErrorCode = "apm_expired"
	ErrAPMPending         ErrorCode = "apm_pending"

	// Errores de webhook.
	ErrWebhookSignature   ErrorCode = "webhook_signature_invalid"
	ErrWebhookParse       ErrorCode = "webhook_parse_error"

	// Error genérico no clasificado.
	ErrUnknown            ErrorCode = "unknown_error"

	// Error interno del SDK/adapter (bug, decode, etc.).
	ErrInternal           ErrorCode = "internal_error"
)

// NewError constructor conveniente.
func NewError(code ErrorCode, cat ErrorCategory, provider ProviderID, msg string) *NormalizedError {
	return &NormalizedError{
		Code:     code,
		Category: cat,
		Message:  msg,
		Provider: provider,
	}
}

// NewDecline constructor para rechazos de tarjeta.
func NewDecline(code ErrorCode, provider ProviderID, decline, providerCode, providerMsg string) *NormalizedError {
	return &NormalizedError{
		Code:           code,
		Category:       CategoryDecline,
		Message:        fmt.Sprintf("card declined: %s", decline),
		Provider:       provider,
		ProviderCode:   providerCode,
		ProviderMessage: providerMsg,
		DeclineCode:    decline,
		Retryable:      false,
	}
}

// NewTransient constructor para errores transitorios (retryable).
func NewTransient(code ErrorCode, provider ProviderID, msg string, cause error) *NormalizedError {
	return &NormalizedError{
		Code:      code,
		Category:  CategoryNetwork,
		Message:   msg,
		Provider:  provider,
		Retryable: true,
		cause:     cause,
	}
}

// IsRetryable helper para el cascading.
func IsRetryable(err error) bool {
	var ne *NormalizedError
	if errors.As(err, &ne) {
		return ne.Retryable
	}
	return false
}

// AsNormalized devuelve el NormalizedError si err lo envuelve, o nil.
func AsNormalized(err error) *NormalizedError {
	var ne *NormalizedError
	if errors.As(err, &ne) {
		return ne
	}
	return nil
}

// Sentinel errors del Core.
var (
	ErrUnsupportedOp = NewError(ErrUnsupportedOperation, CategoryUnsupported, "", "operation not supported by provider")
)
