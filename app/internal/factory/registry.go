// Package factory implementa el registro y construcción de adapters por
// proveedor. Es el punto de extensión: agregar una pasarela nueva = registrar
// un constructor acá, sin tocar el Core ni el código de negocio.
package factory

import (
	"context"
	"fmt"
	"sync"

	"github.com/qampu/pop/internal/core"
)

// AdapterConstructor construye una instancia de Gateway. Recibe el
// TenantContext ya resuelto (con credenciales desencriptadas) para que el
// adapter configure su HTTP client por petición.
//
// Los adapters NO deben mantener estado global ni cachear credenciales:
// cada request construye su propio adapter a través de esta factory.
type AdapterConstructor func(tctx *core.TenantContext) (core.Gateway, error)

// adapterEntry registra constructor + capabilities estáticas.
// Las capabilities son metadata del proveedor (países, monedas, métodos)
// que NO requiere credenciales: se consultan para routing sin construir
// el adapter completo.
type adapterEntry struct {
	build  AdapterConstructor
	caps   core.Capabilities
}

// Registry mantiene el mapeo ProviderID -> adapterEntry.
// Es thread-safe y se inicializa una vez al arrancar el proceso.
type Registry struct {
	mu       sync.RWMutex
	builders map[core.ProviderID]adapterEntry
}

// NewRegistry crea un registry vacío.
func NewRegistry() *Registry {
	return &Registry{builders: make(map[core.ProviderID]adapterEntry)}
}

// Register asocia un constructor + capabilities a un ProviderID. Idempotente:
// re-registrar reemplaza el anterior (útil para tests).
func (r *Registry) Register(p core.ProviderID, caps core.Capabilities, c AdapterConstructor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[p] = adapterEntry{build: c, caps: caps}
}

// MustRegister paniquea si Register falla (uso en init()).
func (r *Registry) MustRegister(p core.ProviderID, caps core.Capabilities, c AdapterConstructor) {
	if err := r.RegisterE(p, caps, c); err != nil {
		panic(err)
	}
}

// RegisterE devuelve error si el constructor es nil.
func (r *Registry) RegisterE(p core.ProviderID, caps core.Capabilities, c AdapterConstructor) error {
	if c == nil {
		return fmt.Errorf("pop: nil constructor for provider %s", p)
	}
	r.Register(p, caps, c)
	return nil
}

// Providers lista los proveedores registrados.
func (r *Registry) Providers() []core.ProviderID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ProviderID, 0, len(r.builders))
	for p := range r.builders {
		out = append(out, p)
	}
	return out
}

// Capabilities devuelve las capabilities estáticas de un proveedor sin
// construir el adapter. Usado por el router para filtrar candidatos.
func (r *Registry) Capabilities(p core.ProviderID) (core.Capabilities, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.builders[p]
	if !ok {
		return core.Capabilities{}, false
	}
	return e.caps, true
}

// Build construye un adapter para (provider) usando el TenantContext dado.
// Valida el contexto antes de invocar al constructor.
func (r *Registry) Build(tctx *core.TenantContext) (core.Gateway, error) {
	if err := tctx.Validate(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	e, ok := r.builders[tctx.Provider]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("pop: no adapter registered for provider %s", tctx.Provider)
	}
	return e.build(tctx)
}

// BuildFromCredentials resuelve credenciales via CredentialResolver y
// construye el adapter en un solo paso. Es el flujo canónico del SDK:
//
//	gw, err := registry.BuildFromCredentials(ctx, resolver, tenantID, provider, mode)
func (r *Registry) BuildFromCredentials(
	ctx context.Context,
	resolver core.CredentialResolver,
	tenantID string,
	provider core.ProviderID,
	mode core.Environment,
) (core.Gateway, error) {
	tctx, err := resolver.Resolve(ctx, tenantID, provider, mode)
	if err != nil {
		return nil, fmt.Errorf("pop: resolve credentials: %w", err)
	}
	return r.Build(tctx)
}

// Default es el registry global usado por el SDK público (pkg/pop).
// Se puebla en init() de cada adapter cuando se importan con blank import:
//
//	import _ "github.com/qampu/pop/internal/adapters/stripe"
//	import _ "github.com/qampu/pop/internal/adapters/mock"
//
// Cada adapter llama a factory.Default.Register(...) en su init().
var Default = NewRegistry()
