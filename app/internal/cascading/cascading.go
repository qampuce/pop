// Package cascading implementa reintentos transparentes y fallback entre
// proveedores. Si el proveedor primario falla con un error retryable
// (timeout, 5xx, rate limit), el cascading prueba el siguiente proveedor
// de la lista devuelta por el Router.
//
// No reintenta errores no-retryables (card_declined, insufficient_funds):
// esos son definitivos y devolverlos al caller permite que el código de
// negocio reaccione (ej. pedir otra tarjeta).
package cascading

import (
	"context"
	"fmt"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

// Policy controla el comportamiento del cascading.
type Policy struct {
	// MaxAttempts por operación (incluye el primer intento). Default 3.
	MaxAttempts int
	// Backoff inicial entre reintentos al mismo proveedor. Default 200ms.
	InitialBackoff time.Duration
	// MaxBackoff cap del backoff exponencial. Default 2s.
	MaxBackoff time.Duration
	// CrossProvider true = si el primario falla retryable, probar el
	// siguiente proveedor de la lista del router. Default true.
	CrossProvider bool
}

// DefaultPolicy sensible para la mayoría de los casos.
var DefaultPolicy = Policy{
	MaxAttempts:    3,
	InitialBackoff: 200 * time.Millisecond,
	MaxBackoff:     2 * time.Second,
	CrossProvider:  true,
}

// Cascader ejecuta una operación contra una lista ordenada de proveedores
// con reintentos y fallback.
type Cascader struct {
	registry *factory.Registry
	policy   Policy
}

// NewCascader construye un cascader.
func NewCascader(r *factory.Registry, p Policy) *Cascader {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = 200 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 2 * time.Second
	}
	return &Cascader{registry: r, policy: p}
}

// Op es la operación a ejecutar contra un Gateway. Devuelve resultado T
// o error. El cascader la invoca con cada adapter construido.
//
// Como Go no permite type parameters en métodos, Run es una función libre
// genérica que recibe el *Cascader explícitamente.
type Op[T any] func(ctx context.Context, gw core.Gateway) (T, error)

// Run ejecuta op contra cada proveedor en providers (ordenados por el router)
// con la política de reintentos. Devuelve el primer resultado exitoso o el
// último error.
//
// Es función libre genérica (no método) porque Go no soporta type parameters
// en métodos. Uso:
//
//	result, err := cascading.Run(ctx, c.cascader, resolver, tenantID, mode, providers, op)
//
// Flujo:
//  1. Para cada provider en providers:
//     a. Construir adapter via registry.BuildFromCredentials.
//     b. Reintentar op hasta MaxAttempts con backoff exponencial.
//     c. Si op devuelve error NO retryable → devolver inmediatamente
//        (no tiene sentido probar otro provider para un card_declined).
//     d. Si error retryable y quedan providers → siguiente provider.
//  2. Si todos fallan → devolver último error.
func Run[T any](
	ctx context.Context,
	c *Cascader,
	resolver core.CredentialResolver,
	tenantID string,
	mode core.Environment,
	providers []core.ProviderID,
	op Op[T],
) (T, error) {
	var zero T
	var lastErr error

	for _, provider := range providers {
		result, err := runSingleProvider(ctx, c, resolver, tenantID, provider, mode, op)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !core.IsRetryable(err) {
			return zero, err // error definitivo, no probar otro provider
		}
		if !c.policy.CrossProvider {
			return zero, err
		}
		// error retryable → probar siguiente provider
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("pop: cascading exhausted with no error")
	}
	return zero, lastErr
}

// runSingleProvider ejecuta op contra un único proveedor con reintentos.
func runSingleProvider[T any](
	ctx context.Context,
	c *Cascader,
	resolver core.CredentialResolver,
	tenantID string,
	provider core.ProviderID,
	mode core.Environment,
	op Op[T],
) (T, error) {
	var zero T
	var lastErr error
	backoff := c.policy.InitialBackoff

	for attempt := 1; attempt <= c.policy.MaxAttempts; attempt++ {
		gw, err := c.registry.BuildFromCredentials(ctx, resolver, tenantID, provider, mode)
		if err != nil {
			// missing_credentials = el tenant no configuró este provider.
			// Lo marcamos retryable para que el cascading salte al siguiente
			// provider de la lista (cross-provider fallback) en lugar de
			// abortar toda la operación. Si NINGÚN provider tiene
			// credenciales, el último error se devuelve al caller.
			ne := core.NewError(core.ErrMissingCredentials, core.CategoryAuth, provider,
				fmt.Sprintf("cannot build adapter: %v", err))
			ne.Retryable = true
			return zero, ne
		}

		result, err := op(ctx, gw)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// No retryable → no reintentar este provider.
		if !core.IsRetryable(err) {
			return zero, err
		}

		if attempt == c.policy.MaxAttempts {
			break
		}

		// Backoff exponencial con jitter simple (mitad del backoff).
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > c.policy.MaxBackoff {
			backoff = c.policy.MaxBackoff
		}
	}

	return zero, lastErr
}
