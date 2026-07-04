// Package routing implementa el enrutamiento inteligente: dado un request
// (país, moneda, método, monto), elige el mejor proveedor entre los
// registrados según sus Capabilities y reglas de negocio del tenant.
package routing

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

// Router selecciona el proveedor óptimo para una request.
//
// Estrategia de selección:
//  1. Filtra providers por country + currency + method (Capabilities).
//  2. Aplica reglas de RoutingRules del tenant (prioridades, blacklist,
//     fallbacks, cost hints).
//  3. Ordena por prioridad y devuelve el mejor candidato.
//  4. Si no hay candidato, devuelve ErrNoProvider.
type Router struct {
	registry *factory.Registry
}

// NewRouter construye un router sobre un registry dado.
func NewRouter(r *factory.Registry) *Router {
	return &Router{registry: r}
}

// RoutingRules son las preferencias del tenant para enrutamiento.
// Permiten priorizar proveedores por país/método, excluir algunos, o
// definir fallbacks explícitos.
type RoutingRules struct {
	// Priorities por país: map[country][]providerID ordenado de mayor a
	// menor prioridad. Ej: {"PE": ["niubiz","mercadopago","kushki"]}.
	Priorities map[string][]core.ProviderID `json:"priorities,omitempty"`
	// Priorities por método: map[method][]providerID.
	MethodPriorities map[core.PaymentMethod][]core.ProviderID `json:"method_priorities,omitempty"`
	// Blacklist de providers a nunca usar para este tenant.
	Blacklist []core.ProviderID `json:"blacklist,omitempty"`
	// Fallbacks explícitos: si el primario falla, probar estos en orden.
	Fallbacks map[core.ProviderID][]core.ProviderID `json:"fallbacks,omitempty"`
}

// RouteRequest describe qué necesita el router para decidir.
type RouteRequest struct {
	Country   string                  `json:"country"`
	Currency  string                  `json:"currency"`
	Method    core.PaymentMethod      `json:"method"`
	Amount    core.Money              `json:"amount"`
	Rules     *RoutingRules           `json:"rules,omitempty"`
}

// Route devuelve la lista ordenada de proveedores aptos (mejor primero).
// El caller (típicamente el Orchestrator) itera y prueba en orden, aplicando
// cascading si el primario falla con error retryable.
func (rt *Router) Route(ctx context.Context, req *RouteRequest) ([]core.ProviderID, error) {
	if rt == nil || rt.registry == nil {
		return nil, fmt.Errorf("pop: router not initialized")
	}
	providers := rt.registry.Providers()
	if len(providers) == 0 {
		return nil, ErrNoProvider
	}

	// 1. Filtrar por capabilities.
	candidates := make([]core.ProviderID, 0, len(providers))
	for _, p := range providers {
		gw, err := rt.registry.Build(&core.TenantContext{
			TenantID: "__routing_probe__",
			Provider: p,
			Country:  req.Country,
			Mode:     core.EnvTest,
			Secret:   "__probe__",
		})
		if err != nil {
			continue
		}
		caps := gw.Capabilities()
		if !supportsCountry(caps, req.Country) {
			continue
		}
		if !supportsCurrency(caps, req.Currency) {
			continue
		}
		if !supportsMethod(caps, req.Method) {
			continue
		}
		candidates = append(candidates, p)
	}

	// 2. Aplicar blacklist.
	candidates = applyBlacklist(candidates, req.Rules)

	if len(candidates) == 0 {
		return nil, ErrNoProvider
	}

	// 3. Ordenar por prioridades del tenant.
	ordered := orderByPriority(candidates, req)

	return ordered, nil
}

// supportsCountry helper.
func supportsCountry(c core.Capabilities, country string) bool {
	if len(c.Countries) == 0 {
		return true // global
	}
	for _, cc := range c.Countries {
		if strings.EqualFold(cc, country) {
			return true
		}
	}
	return false
}

// supportsCurrency helper.
func supportsCurrency(c core.Capabilities, currency string) bool {
	if len(c.Currencies) == 0 {
		return true
	}
	for _, cu := range c.Currencies {
		if strings.EqualFold(cu, currency) {
			return true
		}
	}
	return false
}

// supportsMethod helper.
func supportsMethod(c core.Capabilities, m core.PaymentMethod) bool {
	for _, mm := range c.Methods {
		if mm == m {
			return true
		}
	}
	return false
}

// applyBlacklist elimina providers en blacklist.
func applyBlacklist(candidates []core.ProviderID, rules *RoutingRules) []core.ProviderID {
	if rules == nil || len(rules.Blacklist) == 0 {
		return candidates
	}
	bl := make(map[core.ProviderID]bool, len(rules.Blacklist))
	for _, p := range rules.Blacklist {
		bl[p] = true
	}
	out := candidates[:0]
	for _, c := range candidates {
		if !bl[c] {
			out = append(out, c)
		}
	}
	return out
}

// orderByPriority ordena candidatos según RoutingRules (país > método).
// Los que no aparecen en prioridades van al final en orden estable.
func orderByPriority(candidates []core.ProviderID, req *RouteRequest) []core.ProviderID {
	if req.Rules == nil {
		return candidates
	}
	prio := make(map[core.ProviderID]int, len(candidates))
	for i, p := range candidates {
		prio[p] = len(candidates) + i // base: orden de registro
	}

	// Prioridad por método (más específica) sobreescribe país.
	if req.Rules.MethodPriorities != nil {
		if list, ok := req.Rules.MethodPriorities[req.Method]; ok {
			for i, p := range list {
				prio[p] = i
			}
		}
	}
	if req.Rules.Priorities != nil {
		if list, ok := req.Rules.Priorities[req.Country]; ok {
			for i, p := range list {
				prio[p] = i
			}
		}
	}

	sorted := make([]core.ProviderID, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		return prio[sorted[i]] < prio[sorted[j]]
	})
	return sorted
}

// ErrNoProvider cuando ningún proveedor registrado soporta la request.
var ErrNoProvider = fmt.Errorf("pop: no provider available for the requested country/currency/method")
