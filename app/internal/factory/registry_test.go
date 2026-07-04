package factory

import (
	"context"
	"testing"

	"github.com/qampu/pop/internal/core"
)

type stubGW struct{}

func (stubGW) Provider() core.ProviderID                                       { return "stub" }
func (stubGW) Capabilities() core.Capabilities                                 { return core.Capabilities{} }
func (stubGW) Tokenize(context.Context, *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	return nil, nil
}
func (stubGW) Authorize(context.Context, *core.AuthorizeRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (stubGW) Capture(context.Context, *core.CaptureRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (stubGW) Charge(context.Context, *core.ChargeRequest) (*core.PaymentResult, error) {
	return nil, nil
}
func (stubGW) Refund(context.Context, *core.RefundRequest) (*core.RefundResult, error) {
	return nil, nil
}
func (stubGW) Void(context.Context, *core.VoidRequest) (*core.PaymentResult, error) {
	return nil, nil
}

func TestRegisterAndProviders(t *testing.T) {
	r := NewRegistry()
	r.Register("a", core.Capabilities{Countries: []string{"PE"}}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	r.Register("b", core.Capabilities{Countries: []string{"BR"}}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	pros := r.Providers()
	if len(pros) != 2 {
		t.Errorf("expected 2 providers, got %d", len(pros))
	}
}

func TestRegisterERejectsNilConstructor(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterE("a", core.Capabilities{}, nil); err == nil {
		t.Fatal("expected error on nil constructor")
	}
}

func TestMustRegisterPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil constructor")
		}
	}()
	r := NewRegistry()
	r.MustRegister("a", core.Capabilities{}, nil)
}

func TestCapabilities(t *testing.T) {
	r := NewRegistry()
	caps := core.Capabilities{Countries: []string{"PE"}, Methods: []core.PaymentMethod{core.MethodCard}}
	r.Register("a", caps, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	got, ok := r.Capabilities("a")
	if !ok {
		t.Fatal("expected capabilities for a")
	}
	if len(got.Countries) != 1 || got.Countries[0] != "PE" {
		t.Errorf("unexpected caps: %+v", got)
	}
	if _, ok := r.Capabilities("missing"); ok {
		t.Error("expected missing capabilities to return false")
	}
}

func TestBuildInvalidContext(t *testing.T) {
	r := NewRegistry()
	r.Register("a", core.Capabilities{}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	// TenantContext sin campos obligatorios → Validate() falla.
	_, err := r.Build(&core.TenantContext{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestBuildUnknownProvider(t *testing.T) {
	r := NewRegistry()
	_, err := r.Build(&core.TenantContext{
		TenantID: "tnt", Provider: "missing", Country: "PE", Mode: core.EnvTest, Secret: "x",
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestBuildSuccess(t *testing.T) {
	r := NewRegistry()
	r.Register("a", core.Capabilities{}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	gw, err := r.Build(&core.TenantContext{
		TenantID: "tnt", Provider: "a", Country: "PE", Mode: core.EnvTest, Secret: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gw == nil {
		t.Fatal("expected non-nil gateway")
	}
}

// stubResolver para BuildFromCredentials.
type stubResolver struct {
	tctx *core.TenantContext
	err  error
}

func (s stubResolver) Resolve(ctx context.Context, tenantID string, provider core.ProviderID, mode core.Environment) (*core.TenantContext, error) {
	return s.tctx, s.err
}

func TestBuildFromCredentials(t *testing.T) {
	r := NewRegistry()
	r.Register("a", core.Capabilities{}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	resolver := stubResolver{tctx: &core.TenantContext{
		TenantID: "tnt", Provider: "a", Country: "PE", Mode: core.EnvTest, Secret: "x",
	}}
	gw, err := r.BuildFromCredentials(context.Background(), resolver, "tnt", "a", core.EnvTest)
	if err != nil {
		t.Fatal(err)
	}
	if gw == nil {
		t.Fatal("expected non-nil gateway")
	}
}

func TestBuildFromCredentialsResolveError(t *testing.T) {
	r := NewRegistry()
	r.Register("a", core.Capabilities{}, func(tctx *core.TenantContext) (core.Gateway, error) {
		return stubGW{}, nil
	})
	resolver := stubResolver{err: core.ErrCredentialsNotFound}
	_, err := r.BuildFromCredentials(context.Background(), resolver, "tnt", "a", core.EnvTest)
	if err == nil {
		t.Fatal("expected error from resolver")
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	r := NewRegistry()
	c1 := core.Capabilities{Countries: []string{"PE"}}
	c2 := core.Capabilities{Countries: []string{"BR"}}
	r.Register("a", c1, func(tctx *core.TenantContext) (core.Gateway, error) { return stubGW{}, nil })
	r.Register("a", c2, func(tctx *core.TenantContext) (core.Gateway, error) { return stubGW{}, nil })
	got, _ := r.Capabilities("a")
	if len(got.Countries) != 1 || got.Countries[0] != "BR" {
		t.Errorf("re-register should replace: %+v", got)
	}
	if len(r.Providers()) != 1 {
		t.Errorf("expected 1 provider after re-register, got %d", len(r.Providers()))
	}
}
