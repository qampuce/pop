package vault

import (
	"context"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
)

func TestStoreAndGet(t *testing.T) {
	v := NewMemoryVault()
	tok := &StoredToken{
		ID:            "tok_1",
		TenantID:      "tnt_1",
		BuyerID:       "buy_1",
		Provider:      core.ProviderStripe,
		ProviderToken: "pm_xyz",
		Method:        core.MethodCard,
		Last4:         "4242",
		Brand:         "visa",
	}
	if err := v.Store(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	got, err := v.Get(context.Background(), "tnt_1", "buy_1", "tok_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderToken != "pm_xyz" {
		t.Errorf("expected pm_xyz, got %s", got.ProviderToken)
	}
	if got.Last4 != "4242" {
		t.Errorf("expected 4242, got %s", got.Last4)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set on Store")
	}
}

func TestGetNotFound(t *testing.T) {
	v := NewMemoryVault()
	_, err := v.Get(context.Background(), "tnt_1", "buy_1", "missing")
	if err != ErrTokenNotFound {
		t.Errorf("expected ErrTokenNotFound, got %v", err)
	}
}

func TestGetBuyerMismatch(t *testing.T) {
	v := NewMemoryVault()
	v.Store(context.Background(), &StoredToken{
		ID: "tok_1", TenantID: "tnt_1", BuyerID: "buy_1",
		Provider: core.ProviderStripe, ProviderToken: "pm",
		Method: core.MethodCard,
	})
	_, err := v.Get(context.Background(), "tnt_1", "buy_other", "tok_1")
	if err != ErrTokenNotFound {
		t.Errorf("expected ErrTokenNotFound on buyer mismatch, got %v", err)
	}
}

func TestGetIgnoresBuyerWhenEmpty(t *testing.T) {
	v := NewMemoryVault()
	v.Store(context.Background(), &StoredToken{
		ID: "tok_1", TenantID: "tnt_1", BuyerID: "buy_1",
		Provider: core.ProviderStripe, ProviderToken: "pm",
		Method: core.MethodCard,
	})
	got, err := v.Get(context.Background(), "tnt_1", "", "tok_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BuyerID != "buy_1" {
		t.Errorf("expected buy_1, got %s", got.BuyerID)
	}
}

func TestStoreInvalid(t *testing.T) {
	v := NewMemoryVault()
	cases := []*StoredToken{
		nil,
		{ID: "tok", TenantID: "", Provider: core.ProviderStripe},
		{ID: "", TenantID: "tnt", Provider: core.ProviderStripe},
	}
	for i, tc := range cases {
		if err := v.Store(context.Background(), tc); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestList(t *testing.T) {
	v := NewMemoryVault()
	v.Store(context.Background(), &StoredToken{ID: "t1", TenantID: "tnt", BuyerID: "b1", Provider: core.ProviderStripe, Method: core.MethodCard})
	v.Store(context.Background(), &StoredToken{ID: "t2", TenantID: "tnt", BuyerID: "b1", Provider: core.ProviderStripe, Method: core.MethodCard})
	v.Store(context.Background(), &StoredToken{ID: "t3", TenantID: "tnt", BuyerID: "b2", Provider: core.ProviderStripe, Method: core.MethodCard})
	v.Store(context.Background(), &StoredToken{ID: "t4", TenantID: "other", BuyerID: "b1", Provider: core.ProviderStripe, Method: core.MethodCard})

	got, err := v.List(context.Background(), "tnt", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 tokens for tnt/b1, got %d", len(got))
	}
	// List all tenant tokens (empty buyer).
	got, err = v.List(context.Background(), "tnt", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 tokens for tnt, got %d", len(got))
	}
}

func TestDelete(t *testing.T) {
	v := NewMemoryVault()
	v.Store(context.Background(), &StoredToken{ID: "tok", TenantID: "tnt", Provider: core.ProviderStripe, Method: core.MethodCard})
	if err := v.Delete(context.Background(), "tnt", "tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(context.Background(), "tnt", "", "tok"); err != ErrTokenNotFound {
		t.Errorf("expected ErrTokenNotFound after delete, got %v", err)
	}
	// Delete inexistente no errora.
	if err := v.Delete(context.Background(), "tnt", "missing"); err != nil {
		t.Errorf("delete missing should not error, got %v", err)
	}
}

func TestStorePreservesCreatedAt(t *testing.T) {
	v := NewMemoryVault()
	custom := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	v.Store(context.Background(), &StoredToken{
		ID: "tok", TenantID: "tnt", Provider: core.ProviderStripe,
		Method: core.MethodCard, CreatedAt: custom,
	})
	got, _ := v.Get(context.Background(), "tnt", "", "tok")
	if !got.CreatedAt.Equal(custom) {
		t.Errorf("expected CreatedAt preserved %v, got %v", custom, got.CreatedAt)
	}
}
