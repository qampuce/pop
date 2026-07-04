package core_test

import (
	"context"
	"testing"

	"github.com/qampu/pop/internal/adapters/mock"
	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/routing"
)

// TestTenantContextValidate verifica validación de campos obligatorios.
func TestTenantContextValidate(t *testing.T) {
	cases := []struct {
		name string
		tctx *core.TenantContext
		ok   bool
	}{
		{"nil", nil, false},
		{"empty tenant", &core.TenantContext{Provider: core.ProviderStripe, Country: "US", Mode: core.EnvTest, Secret: "x"}, false},
		{"empty provider", &core.TenantContext{TenantID: "t1", Country: "US", Mode: core.EnvTest, Secret: "x"}, false},
		{"empty country", &core.TenantContext{TenantID: "t1", Provider: core.ProviderStripe, Mode: core.EnvTest, Secret: "x"}, false},
		{"bad mode", &core.TenantContext{TenantID: "t1", Provider: core.ProviderStripe, Country: "US", Mode: "staging", Secret: "x"}, false},
		{"empty secret", &core.TenantContext{TenantID: "t1", Provider: core.ProviderStripe, Country: "US", Mode: core.EnvTest}, false},
		{"valid", &core.TenantContext{TenantID: "t1", Provider: core.ProviderStripe, Country: "US", Mode: core.EnvTest, Secret: "x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tctx.Validate()
			if c.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// TestCredentialVaultRoundTrip verifica encriptación/desencriptación AES-256-GCM.
func TestCredentialVaultRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	v, err := core.NewCredentialVault(key)
	if err != nil {
		t.Fatal(err)
	}
	tctx := &core.TenantContext{
		TenantID:      "tnt_abc",
		Provider:      core.ProviderStripe,
		Country:       "PE",
		Mode:          core.EnvLive,
		PublicKey:     "pk_live_xxx",
		Secret:        "sk_live_secret_value",
		WebhookSecret: "whsec_yyy",
		Metadata:      map[string]string{"env": "prod"},
	}
	if err := v.Store(tctx); err != nil {
		t.Fatal(err)
	}
	got, err := v.Resolve(context.Background(), "tnt_abc", core.ProviderStripe, core.EnvLive)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "sk_live_secret_value" {
		t.Errorf("secret mismatch: %s", got.Secret)
	}
	if got.WebhookSecret != "whsec_yyy" {
		t.Errorf("webhook secret mismatch: %s", got.WebhookSecret)
	}
	if got.Country != "PE" {
		t.Errorf("country mismatch: %s", got.Country)
	}
}

// TestCredentialVaultNotFound verifica error cuando no hay credenciales.
func TestCredentialVaultNotFound(t *testing.T) {
	key := make([]byte, 32)
	v, _ := core.NewCredentialVault(key)
	_, err := v.Resolve(context.Background(), "ghost", core.ProviderStripe, core.EnvTest)
	if err != core.ErrCredentialsNotFound {
		t.Fatalf("expected ErrCredentialsNotFound, got %v", err)
	}
}

// TestNormalizedError verifica la jerarquía de errores.
func TestNormalizedError(t *testing.T) {
	ne := core.NewDecline(core.ErrInsufficientFunds, core.ProviderStripe, "insufficient_funds", "card_declined", "not enough")
	if core.IsRetryable(ne) {
		t.Error("decline should NOT be retryable")
	}
	if ne.Code != core.ErrInsufficientFunds {
		t.Errorf("code mismatch: %s", ne.Code)
	}
}

// TestRouterAndFactory integra factory + router con el adapter mock.
func TestRouterAndFactory(t *testing.T) {
	reg := factory.NewRegistry()
	reg.Register(mock.Provider, mock.Caps, mock.New)

	rt := routing.NewRouter(reg)
	providers, err := rt.Route(context.Background(), &routing.RouteRequest{
		Country:  "PE",
		Currency: "PEN",
		Method:   core.MethodCard,
		Amount:   core.Money{Amount: 1000, Currency: "PEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0] != mock.Provider {
		t.Fatalf("expected [mock], got %v", providers)
	}

	// Construir adapter via factory.
	gw, err := reg.Build(&core.TenantContext{
		TenantID: "t1", Provider: mock.Provider, Country: "PE", Mode: core.EnvTest, Secret: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := gw.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_1",
		Amount:        core.Money{Amount: 1000, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
		Capture:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", res.Status)
	}
	if res.TenantID != "t1" {
		t.Errorf("tenant isolation broken: %s", res.TenantID)
	}
}

// TestIdempotencyKey verifica generación determinística de claves.
func TestIdempotencyKey(t *testing.T) {
	tctx := &core.TenantContext{TenantID: "t1", IdempotencyPrefix: "p"}
	k1 := tctx.IdempotencyKey("charge", "order_1")
	k2 := tctx.IdempotencyKey("charge", "order_1")
	if k1 != k2 {
		t.Errorf("non-deterministic: %s != %s", k1, k2)
	}
	if k1 != "p:charge:order_1" {
		t.Errorf("bad format: %s", k1)
	}
}
