package pop_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/pkg/pop"
)

// newTestClient construye un Client con vault in-memory + mock adapter.
func newTestClient(t *testing.T) *pop.Client {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	vault, err := core.NewCredentialVault(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Store(&core.TenantContext{
		TenantID: "tnt_1", Provider: "mock", Country: "PE",
		Mode: core.EnvTest, Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := pop.New(pop.Config{Credentials: vault})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestClientChargeHappyPath verifica el flujo end-to-end del SDK público.
func TestClientChargeHappyPath(t *testing.T) {
	c := newTestClient(t)
	res, err := c.Charge(context.Background(), &pop.ChargeRequestExt{
		ChargeRequest: pop.ChargeRequest{
			Reference:     "order_42",
			Amount:        pop.Money{Amount: 19990, Currency: "PEN"},
			Method:        pop.MethodCard,
			ProviderToken: "tok_4242",
			Capture:       true,
		},
		TenantID: "tnt_1",
		Mode:     pop.Test,
		Country:  "PE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != pop.StatusCaptured {
		t.Errorf("expected captured, got %s", res.Status)
	}
	if res.TenantID != "tnt_1" {
		t.Errorf("tenant isolation broken: %s", res.TenantID)
	}
	if res.Provider != "mock" {
		t.Errorf("expected mock provider, got %s", res.Provider)
	}
}

// TestClientTokenize verifica tokenize via el SDK público.
func TestClientTokenize(t *testing.T) {
	c := newTestClient(t)
	out, err := c.Tokenize(context.Background(), "tnt_1", "mock", pop.Test, &pop.TokenizeRequest{
		Method: pop.MethodCard,
		Card: &pop.CardToken{
			Token: "tok_front", Last4: "4242", Brand: "visa",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderToken == "" {
		t.Error("empty provider token")
	}
	if out.Last4 != "4242" {
		t.Errorf("last4 mismatch: %s", out.Last4)
	}
}

// TestClientAuthorizeCaptureVoid verifica el flujo auth-split.
func TestClientAuthorizeCaptureVoid(t *testing.T) {
	c := newTestClient(t)
	auth, err := c.Authorize(context.Background(), &pop.AuthorizeRequestExt{
		AuthorizeRequest: pop.AuthorizeRequest{
			Reference:     "order_43",
			Amount:        pop.Money{Amount: 5000, Currency: "PEN"},
			Method:        pop.MethodCard,
			ProviderToken: "tok_x",
		},
		TenantID: "tnt_1",
		Mode:     pop.Test,
		Country:  "PE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Status != pop.StatusAuthorized {
		t.Errorf("expected authorized, got %s", auth.Status)
	}

	cap, err := c.Capture(context.Background(), &pop.CaptureRequestExt{
		CaptureRequest: pop.CaptureRequest{
			AuthorizationID: auth.ID,
			Amount:          pop.Money{Amount: 5000, Currency: "PEN"},
		},
		TenantID: "tnt_1",
		Mode:     pop.Test,
		Provider: "mock",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.Status != pop.StatusCaptured {
		t.Errorf("expected captured, got %s", cap.Status)
	}
}

// TestClientRefund verifica reembolso.
func TestClientRefund(t *testing.T) {
	c := newTestClient(t)
	res, err := c.Refund(context.Background(), &pop.RefundRequestExt{
		RefundRequest: pop.RefundRequest{
			PaymentID: "pay_1",
			Amount:    pop.Money{Amount: 1000, Currency: "PEN"},
			Reason:    pop.RefundRequestedByCustomer,
		},
		TenantID: "tnt_1",
		Mode:     pop.Test,
		Provider: "mock",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != pop.StatusRefunded {
		t.Errorf("expected refunded, got %s", res.Status)
	}
}

// TestClientNewRequiresCredentials verifica guard de config.
func TestClientNewRequiresCredentials(t *testing.T) {
	_, err := pop.New(pop.Config{})
	if err == nil {
		t.Fatal("expected error when Credentials is nil")
	}
}

// TestClientProcessWebhook verifica el flujo end-to-end del webhook normalizer:
// el mock adapter verifica la firma, resuelve el tenant y normaliza el payload
// a un Event canónico.
func TestClientProcessWebhook(t *testing.T) {
	c := newTestClient(t)

	body := `{"type":"payment.captured","payment_id":"mock_pay_order_42","reference":"order_42","status":"captured","amount":19990,"currency":"PEN","provider_event_id":"evt_123","created_at":"2026-01-02T15:04:05Z"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/mock", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Mock-Tenant", "tnt_1")
	req.Header.Set("X-Mock-Signature", "mock_secret")

	ev, err := c.ProcessWebhook(context.Background(), "mock", pop.Test, req)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != pop.EventPaymentCaptured {
		t.Errorf("expected payment.captured, got %s", ev.Type)
	}
	if ev.TenantID != "tnt_1" {
		t.Errorf("expected tenant tnt_1, got %s", ev.TenantID)
	}
	if ev.Provider != "mock" {
		t.Errorf("expected provider mock, got %s", ev.Provider)
	}
	if ev.PaymentID != "mock_pay_order_42" {
		t.Errorf("expected payment_id mock_pay_order_42, got %s", ev.PaymentID)
	}
	if ev.Status != pop.StatusCaptured {
		t.Errorf("expected status captured, got %s", ev.Status)
	}
	if ev.Amount.Amount != 19990 || ev.Amount.Currency != "PEN" {
		t.Errorf("amount mismatch: %+v", ev.Amount)
	}
	if ev.ProviderEventID != "evt_123" {
		t.Errorf("expected provider_event_id evt_123, got %s", ev.ProviderEventID)
	}
}

// TestClientProcessWebhookBadSignature verifica que un signature inválido
// devuelve error de firma.
func TestClientProcessWebhookBadSignature(t *testing.T) {
	c := newTestClient(t)

	body := `{"type":"payment.captured"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/mock", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Mock-Tenant", "tnt_1")
	req.Header.Set("X-Mock-Signature", "wrong")

	if _, err := c.ProcessWebhook(context.Background(), "mock", pop.Test, req); err == nil {
		t.Fatal("expected signature error, got nil")
	}
}

// TestClientProcessWebhookMissingTenant verifica que falta el header de tenant.
func TestClientProcessWebhookMissingTenant(t *testing.T) {
	c := newTestClient(t)

	body := `{"type":"payment.captured"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/mock", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.ProcessWebhook(context.Background(), "mock", pop.Test, req); err == nil {
		t.Fatal("expected missing tenant error, got nil")
	}
}
