package cascading_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qampu/pop/internal/adapters/mock"
	"github.com/qampu/pop/internal/cascading"
	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

// resolverFromVault helper: usa CredentialVault in-memory con un mock pre-stored.
func newResolver(t *testing.T) core.CredentialResolver {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := core.NewCredentialVault(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Store(&core.TenantContext{
		TenantID: "t1", Provider: mock.Provider, Country: "PE",
		Mode: core.EnvTest, Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCascadingSuccess verifica el happy path: primer provider funciona.
func TestCascadingSuccess(t *testing.T) {
	reg := factory.NewRegistry()
	reg.Register(mock.Provider, mock.Caps, mock.New)
	c := cascading.NewCascader(reg, cascading.Policy{MaxAttempts: 2, CrossProvider: true})

	res, err := cascading.Run(context.Background(), c, newResolver(t), "t1", core.EnvTest,
		[]core.ProviderID{mock.Provider},
		func(ctx context.Context, gw core.Gateway) (*core.PaymentResult, error) {
			return gw.Charge(ctx, &core.ChargeRequest{
				Reference: "o1", Amount: core.Money{Amount: 100, Currency: "PEN"},
				Method: core.MethodCard, Capture: true,
			})
		})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", res.Status)
	}
}

// TestCascadingNonRetryable verifica que un error no-retryable se devuelve
// inmediatamente sin probar otro provider.
func TestCascadingNonRetryable(t *testing.T) {
	reg := factory.NewRegistry()
	reg.Register(mock.Provider, mock.Caps, mock.New)
	c := cascading.NewCascader(reg, cascading.Policy{MaxAttempts: 3, CrossProvider: true})

	decline := core.NewDecline(core.ErrCardDeclined, mock.Provider, "card_declined", "x", "y")
	_, err := cascading.Run(context.Background(), c, newResolver(t), "t1", core.EnvTest,
		[]core.ProviderID{mock.Provider, mock.Provider},
		func(ctx context.Context, gw core.Gateway) (*core.PaymentResult, error) {
			return nil, decline
		})
	if !errors.Is(err, decline) {
		t.Fatalf("expected decline, got %v", err)
	}
}

// TestCascadingRetryableThenSuccess verifica backoff + reintento exitoso.
func TestCascadingRetryableThenSuccess(t *testing.T) {
	reg := factory.NewRegistry()
	reg.Register(mock.Provider, mock.Caps, mock.New)
	policy := cascading.Policy{
		MaxAttempts:    3,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
		CrossProvider:  true,
	}
	c := cascading.NewCascader(reg, policy)

	attempts := 0
	res, err := cascading.Run(context.Background(), c, newResolver(t), "t1", core.EnvTest,
		[]core.ProviderID{mock.Provider},
		func(ctx context.Context, gw core.Gateway) (*core.PaymentResult, error) {
			attempts++
			if attempts < 2 {
				return nil, core.NewTransient(core.ErrTimeout, mock.Provider, "timeout", nil)
			}
			return &core.PaymentResult{ID: "ok", Status: core.StatusCaptured}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
	if res.ID != "ok" {
		t.Errorf("expected ok, got %s", res.ID)
	}
}
