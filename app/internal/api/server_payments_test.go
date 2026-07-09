package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/store"
	"github.com/qampu/pop/pkg/pop"
)

func TestHandlePayments_GetByID(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	// Seed un payment de prueba
	payment := &core.PaymentResult{
		ID:        "pay_test_123",
		TenantID:  "demo",
		Status:    core.StatusCaptured,
		Reference: "order_456",
		Amount:    core.Money{Amount: 19990, Currency: "PEN"},
		Provider:  core.ProviderID("mock"),
	}
	st.RecordPayment("charge", payment)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/pay_test_123", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var record store.PaymentRecord
	if err := json.NewDecoder(w.Body).Decode(&record); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if record.ID != "pay_test_123" {
		t.Errorf("expected ID pay_test_123, got %s", record.ID)
	}
}

func TestHandlePayments_GetByID_NotFound(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandlePayments_List(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	// Seed payments de prueba
	payment1 := &core.PaymentResult{
		ID:        "pay_test_1",
		TenantID:  "demo",
		Status:    core.StatusCaptured,
		Reference: "order_1",
		Amount:    core.Money{Amount: 10000, Currency: "PEN"},
		Provider:  core.ProviderID("mock"),
	}
	payment2 := &core.PaymentResult{
		ID:        "pay_test_2",
		TenantID:  "demo",
		Status:    core.StatusFailed,
		Reference: "order_2",
		Amount:    core.Money{Amount: 20000, Currency: "PEN"},
		Provider:  core.ProviderID("mock"),
	}
	st.RecordPayment("charge", payment1)
	st.RecordPayment("charge", payment2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/?tenant_id=demo", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response struct {
		Payments []*store.PaymentRecord `json:"payments"`
		Count    int                    `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Count != 2 {
		t.Errorf("expected 2 payments, got %d", response.Count)
	}
}

func TestHandlePayments_List_WithStatusFilter(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	// Seed payments con diferentes estados
	payment1 := &core.PaymentResult{
		ID:        "pay_test_1",
		TenantID:  "demo",
		Status:    core.StatusCaptured,
		Reference: "order_1",
		Amount:    core.Money{Amount: 10000, Currency: "PEN"},
		Provider:  core.ProviderID("mock"),
	}
	payment2 := &core.PaymentResult{
		ID:        "pay_test_2",
		TenantID:  "demo",
		Status:    core.StatusFailed,
		Reference: "order_2",
		Amount:    core.Money{Amount: 20000, Currency: "PEN"},
		Provider:  core.ProviderID("mock"),
	}
	st.RecordPayment("charge", payment1)
	st.RecordPayment("charge", payment2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/?tenant_id=demo&status=captured", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response struct {
		Payments []*store.PaymentRecord `json:"payments"`
		Count    int                    `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Count != 1 {
		t.Errorf("expected 1 payment with status captured, got %d", response.Count)
	}

	if response.Payments[0].Status != core.StatusCaptured {
		t.Errorf("expected status captured, got %s", response.Payments[0].Status)
	}
}

func TestHandlePayments_List_WithLimit(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	// Seed múltiples payments
	for i := 1; i <= 5; i++ {
		payment := &core.PaymentResult{
			ID:        "pay_test_" + fmt.Sprintf("%d", i),
			TenantID:  "demo",
			Status:    core.StatusCaptured,
			Reference: "order_" + fmt.Sprintf("%d", i),
			Amount:    core.Money{Amount: int64(i * 10000), Currency: "PEN"},
			Provider:  core.ProviderID("mock"),
		}
		st.RecordPayment("charge", payment)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/payments/?tenant_id=demo&limit=3", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response struct {
		Payments []*store.PaymentRecord `json:"payments"`
		Count    int                    `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Count != 3 {
		t.Errorf("expected 3 payments with limit=3, got %d", response.Count)
	}
}

func TestHandlePayments_List_InvalidMethod(t *testing.T) {
	st := store.New()
	client, _ := pop.New(pop.Config{
		Credentials: &mockCredentialResolver{},
	})
	srv := New(client, st)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/payments", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

// mockCredentialResolver para tests
type mockCredentialResolver struct{}

func (m *mockCredentialResolver) Resolve(ctx context.Context, tenantID string, provider core.ProviderID, mode core.Environment) (*core.TenantContext, error) {
	return &core.TenantContext{
		TenantID:      tenantID,
		Provider:      provider,
		Country:       "PE",
		Mode:          mode,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
	}, nil
}

func (m *mockCredentialResolver) Store(ctx context.Context, tc *core.TenantContext) error {
	return nil
}

func (m *mockCredentialResolver) List(ctx context.Context, tenantID string) ([]*core.TenantContext, error) {
	return nil, nil
}

func (m *mockCredentialResolver) Delete(ctx context.Context, tenantID string, provider core.ProviderID) error {
	return nil
}
