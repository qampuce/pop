package routing

import (
	"context"
	"testing"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

// stubGateway minimal para que el router pueda Build() y consultar Capabilities.
type stubGateway struct {
	caps core.Capabilities
}

func (s *stubGateway) Provider() core.ProviderID             { return "stub" }
func (s *stubGateway) Capabilities() core.Capabilities       { return s.caps }
func (s *stubGateway) Tokenize(context.Context, *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	return nil, nil
}
func (s *stubGateway) Authorize(context.Context, *core.AuthorizeRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (s *stubGateway) Capture(context.Context, *core.CaptureRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (s *stubGateway) Charge(context.Context, *core.ChargeRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (s *stubGateway) Refund(context.Context, *core.RefundRequest) (*core.RefundResult, error) {
	return nil, nil
}
func (s *stubGateway) Void(context.Context, *core.VoidRequest) (*core.PaymentResult, error) {
	return nil, nil
}

// newRegistryWithStubs registra N providers con capabilities dadas.
func newRegistryWithStubs(t *testing.T, entries map[core.ProviderID]core.Capabilities) *factory.Registry {
	r := factory.NewRegistry()
	for p, caps := range entries {
		caps := caps
		r.Register(p, caps, func(tctx *core.TenantContext) (core.Gateway, error) {
			return &stubGateway{caps: caps}, nil
		})
	}
	return r
}

func TestRouteFiltersByCountry(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"global": {Countries: nil, Methods: []core.PaymentMethod{core.MethodCard}},
		"pe_only": {Countries: []string{"PE"}, Methods: []core.PaymentMethod{core.MethodCard}},
		"br_only": {Countries: []string{"BR"}, Methods: []core.PaymentMethod{core.MethodCard}},
	})
	rt := NewRouter(r)

	got, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "USD", Method: core.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[core.ProviderID]bool{"global": true, "pe_only": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d candidates, got %d: %v", len(want), len(got), got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected provider %s in PE routing", p)
		}
	}
}

func TestRouteFiltersByMethod(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"card_only": {Methods: []core.PaymentMethod{core.MethodCard}},
		"pix_only":  {Methods: []core.PaymentMethod{core.MethodPix}},
	})
	rt := NewRouter(r)

	got, err := rt.Route(context.Background(), &RouteRequest{
		Country: "BR", Currency: "BRL", Method: core.MethodPix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "pix_only" {
		t.Errorf("expected only pix_only, got %v", got)
	}
}

func TestRouteNoProvider(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"card_only": {Methods: []core.PaymentMethod{core.MethodCard}},
	})
	rt := NewRouter(r)

	_, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "PEN", Method: core.MethodPix,
	})
	if err != ErrNoProvider {
		t.Errorf("expected ErrNoProvider, got %v", err)
	}
}

func TestRouteEmptyRegistry(t *testing.T) {
	rt := NewRouter(factory.NewRegistry())
	_, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "PEN", Method: core.MethodCard,
	})
	if err != ErrNoProvider {
		t.Errorf("expected ErrNoProvider, got %v", err)
	}
}

func TestRouteBlacklist(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"a": {Methods: []core.PaymentMethod{core.MethodCard}},
		"b": {Methods: []core.PaymentMethod{core.MethodCard}},
		"c": {Methods: []core.PaymentMethod{core.MethodCard}},
	})
	rt := NewRouter(r)

	got, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "USD", Method: core.MethodCard,
		Rules: &RoutingRules{Blacklist: []core.ProviderID{"b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p == "b" {
			t.Error("blacklisted provider b should not appear")
		}
	}
	if len(got) != 2 {
		t.Errorf("expected 2 candidates after blacklist, got %d", len(got))
	}
}

func TestRoutePriorityOrdering(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"niubiz":      {Methods: []core.PaymentMethod{core.MethodCard}},
		"mercadopago": {Methods: []core.PaymentMethod{core.MethodCard}},
		"kushki":      {Methods: []core.PaymentMethod{core.MethodCard}},
	})
	rt := NewRouter(r)

	got, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "PEN", Method: core.MethodCard,
		Rules: &RoutingRules{
			Priorities: map[string][]core.ProviderID{
				"PE": {"niubiz", "mercadopago", "kushki"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	want := []core.ProviderID{"niubiz", "mercadopago", "kushki"}
	for i, p := range got {
		if p != want[i] {
			t.Errorf("position %d: expected %s, got %s", i, want[i], p)
		}
	}
}

func TestRouteMethodPriorityOverridesCountry(t *testing.T) {
	r := newRegistryWithStubs(t, map[core.ProviderID]core.Capabilities{
		"a": {Methods: []core.PaymentMethod{core.MethodCard}},
		"b": {Methods: []core.PaymentMethod{core.MethodCard}},
		"c": {Methods: []core.PaymentMethod{core.MethodCard}},
	})
	rt := NewRouter(r)

	got, err := rt.Route(context.Background(), &RouteRequest{
		Country: "PE", Currency: "PEN", Method: core.MethodCard,
		Rules: &RoutingRules{
			Priorities: map[string][]core.ProviderID{
				"PE": {"a", "b", "c"},
			},
			MethodPriorities: map[core.PaymentMethod][]core.ProviderID{
				core.MethodCard: {"c", "a", "b"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// MethodPriorities sobreescribe Priorities por país.
	want := []core.ProviderID{"c", "a", "b"}
	for i, p := range got {
		if p != want[i] {
			t.Errorf("position %d: expected %s, got %s", i, want[i], p)
		}
	}
}

func TestRouteNilRouter(t *testing.T) {
	var rt *Router
	_, err := rt.Route(context.Background(), &RouteRequest{})
	if err == nil {
		t.Fatal("expected error on nil router")
	}
}

func TestSupportsHelpers(t *testing.T) {
	c := core.Capabilities{
		Countries:  []string{"PE", "BR"},
		Currencies: []string{"PEN", "BRL"},
		Methods:    []core.PaymentMethod{core.MethodCard, core.MethodPix},
	}
	if !supportsCountry(c, "pe") { // case-insensitive
		t.Error("supportsCountry should be case-insensitive")
	}
	if supportsCountry(c, "US") {
		t.Error("US not in countries")
	}
	if !supportsCurrency(c, "pen") {
		t.Error("supportsCurrency should be case-insensitive")
	}
	if !supportsMethod(c, core.MethodCard) {
		t.Error("should support card")
	}
	if supportsMethod(c, core.MethodSPEI) {
		t.Error("should not support spei")
	}
	// Vacío = global / todas.
	global := core.Capabilities{}
	if !supportsCountry(global, "ZZ") {
		t.Error("empty countries should be global")
	}
	if !supportsCurrency(global, "XXX") {
		t.Error("empty currencies should support all")
	}
}
