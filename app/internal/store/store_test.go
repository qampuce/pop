package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
)

// TestStoreIndexes verifica que los índices se mantengan correctamente
// al agregar y actualizar registros.
func TestStoreIndexes(t *testing.T) {
	s := New()
	
	// Agregar payments
	p1 := &core.PaymentResult{
		ID:        "pay1",
		TenantID:  "tnt1",
		Provider:  core.ProviderStripe,
		Status:    core.StatusCaptured,
		Amount:    core.Money{Amount: 1000, Currency: "USD"},
		Reference: "ref1",
		CreatedAt: time.Now().UTC(),
	}
	
	p2 := &core.PaymentResult{
		ID:        "pay2",
		TenantID:  "tnt2",
		Provider:  core.ProviderMercadoPago,
		Status:    core.StatusAuthorized,
		Amount:    core.Money{Amount: 2000, Currency: "PEN"},
		Reference: "ref2",
		CreatedAt: time.Now().UTC(),
	}
	
	s.RecordPayment("charge", p1)
	s.RecordPayment("authorize", p2)
	
	// Verificar índice por tenant
	if len(s.byTenant["tnt1"]) != 1 {
		t.Errorf("expected 1 payment for tenant tnt1, got %d", len(s.byTenant["tnt1"]))
	}
	if len(s.byTenant["tnt2"]) != 1 {
		t.Errorf("expected 1 payment for tenant tnt2, got %d", len(s.byTenant["tnt2"]))
	}
	
	// Verificar índice por provider
	if len(s.byProvider[core.ProviderStripe]) != 1 {
		t.Errorf("expected 1 payment for provider stripe, got %d", len(s.byProvider[core.ProviderStripe]))
	}
	if len(s.byProvider[core.ProviderMercadoPago]) != 1 {
		t.Errorf("expected 1 payment for provider mercadopago, got %d", len(s.byProvider[core.ProviderMercadoPago]))
	}
	
	// Verificar índice por status
	if len(s.byStatus[core.StatusCaptured]) != 1 {
		t.Errorf("expected 1 payment for status captured, got %d", len(s.byStatus[core.StatusCaptured]))
	}
	if len(s.byStatus[core.StatusAuthorized]) != 1 {
		t.Errorf("expected 1 payment for status authorized, got %d", len(s.byStatus[core.StatusAuthorized]))
	}
	
	// Actualizar payment cambiando status
	p1.Status = core.StatusRefunded
	s.RecordPayment("refund", p1)
	
	// Verificar que el índice de status se actualizó
	if len(s.byStatus[core.StatusCaptured]) != 0 {
		t.Errorf("expected 0 payments for status captured after update, got %d", len(s.byStatus[core.StatusCaptured]))
	}
	if len(s.byStatus[core.StatusRefunded]) != 1 {
		t.Errorf("expected 1 payment for status refunded after update, got %d", len(s.byStatus[core.StatusRefunded]))
	}
}

// TestListPaymentsWithIndexes verifica que ListPayments use los índices
// correctamente para filtros comunes.
func TestListPaymentsWithIndexes(t *testing.T) {
	s := New()
	
	// Agregar varios payments
	for i := 0; i < 10; i++ {
		provider := core.ProviderStripe
		if i%2 == 0 {
			provider = core.ProviderMercadoPago
		}
		
		status := core.StatusCaptured
		if i%3 == 0 {
			status = core.StatusAuthorized
		} else if i%3 == 1 {
			status = core.StatusFailed
		}
		
		p := &core.PaymentResult{
			ID:        fmt.Sprintf("pay%d", i),
			TenantID:  "tnt1",
			Provider:  provider,
			Status:    status,
			Amount:    core.Money{Amount: int64((i + 1) * 100), Currency: "USD"},
			Reference: fmt.Sprintf("ref%d", i),
			CreatedAt: time.Now().UTC(),
		}
		s.RecordPayment("charge", p)
	}
	
	// Filtro por provider
	stripePayments := s.ListPayments(Filter{Provider: core.ProviderStripe})
	if len(stripePayments) != 5 {
		t.Errorf("expected 5 stripe payments, got %d", len(stripePayments))
	}
	
	// Filtro por status
	capturedPayments := s.ListPayments(Filter{Status: core.StatusCaptured})
	if len(capturedPayments) != 4 {
		t.Errorf("expected 4 captured payments, got %d", len(capturedPayments))
	}
	
	// Filtro por tenant
	tenantPayments := s.ListPayments(Filter{TenantID: "tnt1"})
	if len(tenantPayments) != 10 {
		t.Errorf("expected 10 tenant payments, got %d", len(tenantPayments))
	}
	
	// Filtro combinado (debe usar el índice más específico)
	stripeCaptured := s.ListPayments(Filter{
		Provider: core.ProviderStripe,
		Status:   core.StatusCaptured,
	})
	if len(stripeCaptured) != 2 {
		t.Errorf("expected 2 stripe captured payments, got %d", len(stripeCaptured))
	}
}
