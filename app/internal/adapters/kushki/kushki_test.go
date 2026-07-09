// Package kushki tests para el adapter de Kushki.
package kushki

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
)

func TestNew(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if adapter.Provider() != Provider {
		t.Errorf("Provider() = %v, want %v", adapter.Provider(), Provider)
	}
	caps := adapter.Capabilities()
	if len(caps.Countries) != 2 {
		t.Errorf("Capabilities.Countries length = %d, want 2", len(caps.Countries))
	}
}

func TestTokenize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"tok_test123"}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.TokenizeRequest{
		Method: core.MethodCard,
		Card: &core.CardToken{
			Token: "card_token_123",
			Last4: "4242",
			Brand: "visa",
		},
	}
	resp, err := adapter.Tokenize(context.Background(), req)
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}
	if resp.ProviderToken != "tok_test123" {
		t.Errorf("ProviderToken = %v, want tok_test123", resp.ProviderToken)
	}
	if !resp.Vaulted {
		t.Errorf("Vaulted = false, want true")
	}
}

func TestAuthorize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "auth_123",
			"status": "AUTHORIZED",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z"
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.AuthorizeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test123",
	}
	resp, err := adapter.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if resp.ID != "auth_123" {
		t.Errorf("ID = %v, want auth_123", resp.ID)
	}
	if resp.Status != core.StatusAuthorized {
		t.Errorf("Status = %v, want %v", resp.Status, core.StatusAuthorized)
	}
}

func TestCharge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "charge_123",
			"status": "CAPTURED",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z"
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.ChargeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test123",
		Capture:       true,
	}
	resp, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if resp.ID != "charge_123" {
		t.Errorf("ID = %v, want charge_123", resp.ID)
	}
	if resp.Status != core.StatusCaptured {
		t.Errorf("Status = %v, want %v", resp.Status, core.StatusCaptured)
	}
}

func TestCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "charge_123",
			"status": "CAPTURED",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z"
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.CaptureRequest{
		AuthorizationID: "auth_123",
		Amount:          core.Money{Amount: 10000, Currency: "USD"},
	}
	resp, err := adapter.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if resp.ID != "charge_123" {
		t.Errorf("ID = %v, want charge_123", resp.ID)
	}
	if resp.Status != core.StatusCaptured {
		t.Errorf("Status = %v, want %v", resp.Status, core.StatusCaptured)
	}
}

func TestVoid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "auth_123",
			"status": "VOIDED",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z"
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.VoidRequest{
		AuthorizationID: "auth_123",
		Reason:          "customer_request",
	}
	resp, err := adapter.Void(context.Background(), req)
	if err != nil {
		t.Fatalf("Void() error = %v", err)
	}
	if resp.ID != "auth_123" {
		t.Errorf("ID = %v, want auth_123", resp.ID)
	}
	if resp.Status != core.StatusVoided {
		t.Errorf("Status = %v, want %v", resp.Status, core.StatusVoided)
	}
}

func TestRefund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Private-Merchant-Id") != "test_secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"refundTicketNumber": "refund_123",
			"status": "APPROVED",
			"amount": 100.00
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.RefundRequest{
		PaymentID: "charge_123",
		Amount:    core.Money{Amount: 10000, Currency: "USD"},
		Reason:    core.RefundRequestedByCustomer,
	}
	resp, err := adapter.Refund(context.Background(), req)
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if resp.ID != "refund_123" {
		t.Errorf("ID = %v, want refund_123", resp.ID)
	}
	if resp.Status != core.StatusRefunded {
		t.Errorf("Status = %v, want %v", resp.Status, core.StatusRefunded)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantCode core.ErrorCode
	}{
		{
			name:     "invalid credentials",
			response: `{"code": "K001", "message": "Invalid credentials"}`,
			wantCode: core.ErrInvalidCredentials,
		},
		{
			name:     "card declined",
			response: `{"code": "K004", "message": "Card declined"}`,
			wantCode: core.ErrCardDeclined,
		},
		{
			name:     "insufficient funds",
			response: `{"code": "K005", "message": "Insufficient funds"}`,
			wantCode: core.ErrInsufficientFunds,
		},
		{
			name:     "expired card",
			response: `{"code": "K006", "message": "Expired card"}`,
			wantCode: core.ErrExpiredCard,
		},
		{
			name:     "invalid cvc",
			response: `{"code": "K007", "message": "Invalid CVC"}`,
			wantCode: core.ErrInvalidCVC,
		},
		{
			name:     "invalid number",
			response: `{"code": "K008", "message": "Invalid card number"}`,
			wantCode: core.ErrInvalidNumber,
		},
		{
			name:     "suspected fraud",
			response: `{"code": "K009", "message": "Suspected fraud"}`,
			wantCode: core.ErrSuspectedFraud,
		},
		{
			name:     "do not honor",
			response: `{"code": "K010", "message": "Do not honor"}`,
			wantCode: core.ErrDoNotHonor,
		},
		{
			name:     "processing error",
			response: `{"code": "K011", "message": "Processing error"}`,
			wantCode: core.ErrProcessingError,
		},
		{
			name:     "rate limited",
			response: `{"code": "K012", "message": "Rate limited"}`,
			wantCode: core.ErrRateLimited,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			tctx := &core.TenantContext{
				TenantID: "test",
				Provider: Provider,
				Country:  "EC",
				Mode:     core.EnvTest,
				Secret:   "test_secret",
			}
			adapter, _ := New(tctx)
			adapter.(*Adapter).base = server.URL

			req := &core.ChargeRequest{
				Reference:     "order_123",
				Amount:        core.Money{Amount: 10000, Currency: "USD"},
				Method:        core.MethodCard,
				ProviderToken: "tok_test123",
				Capture:       true,
			}
			_, err := adapter.Charge(context.Background(), req)
			if err == nil {
				t.Fatal("Charge() expected error, got nil")
			}
			nerr, ok := err.(*core.NormalizedError)
			if !ok {
				t.Fatalf("error is not NormalizedError: %T", err)
			}
			if nerr.Code != tt.wantCode {
				t.Errorf("ErrorCode = %v, want %v", nerr.Code, tt.wantCode)
			}
		})
	}
}

func TestWebhookVerification(t *testing.T) {
	body := []byte(`{"event":"charge.success","data":{"ticketNumber":"123"}}`)
	secret := "webhook_secret"

	verifier := &kushkiVerifier{}
	sig := SignWebhook(body, secret)

	headers := http.Header{}
	headers.Set("X-Kushki-Signature", sig)

	if err := verifier.Verify(body, headers, secret); err != nil {
		t.Errorf("Verify() error = %v", err)
	}

	// Test invalid signature
	headers.Set("X-Kushki-Signature", "invalid")
	if err := verifier.Verify(body, headers, secret); err == nil {
		t.Error("Verify() expected error for invalid signature, got nil")
	}
}

func TestWebhookNormalization(t *testing.T) {
	tests := []struct {
		name         string
		body         []byte
		wantType     string
		wantTicket   string
	}{
		{
			name:         "charge success",
			body:         BuildTestWebhook("charge.success", "ticket_123", "CAPTURED", 100.00, "USD"),
			wantType:     "payment.captured",
			wantTicket:   "ticket_123",
		},
		{
			name:         "charge authorization",
			body:         BuildTestWebhook("charge.authorization", "ticket_456", "AUTHORIZED", 50.00, "USD"),
			wantType:     "payment.authorized",
			wantTicket:   "ticket_456",
		},
		{
			name:         "charge declined",
			body:         BuildTestWebhook("charge.declined", "ticket_789", "DECLINED", 75.00, "USD"),
			wantType:     "payment.failed",
			wantTicket:   "ticket_789",
		},
		{
			name:         "refund success",
			body:         BuildTestWebhook("refund.success", "ticket_000", "APPROVED", 25.00, "USD"),
			wantType:     "refund.completed",
			wantTicket:   "ticket_000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizer := &kushkiNormalizer{}
			evt, err := normalizer.Normalize(tt.body)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if evt.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", evt.Type, tt.wantType)
			}
			if evt.Provider != Provider {
				t.Errorf("Provider = %v, want %v", evt.Provider, Provider)
			}
			ticket, ok := evt.Payload["ticketNumber"].(string)
			if !ok || ticket != tt.wantTicket {
				t.Errorf("ticketNumber = %v, want %v", ticket, tt.wantTicket)
			}
		})
	}
}

func TestIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			http.Error(w, "missing idempotency key", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "charge_123",
			"status": "CAPTURED",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z"
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.ChargeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test123",
		Capture:       true,
	}
	_, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
}

func TestAPMRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"ticketNumber": "charge_123",
			"status": "PENDING",
			"amount": 100.00,
			"currency": "USD",
			"createdAt": "2025-01-02T15:04:05.000Z",
			"details": {
				"redirectUrl": "https://kushki.com/redirect/123"
			}
		}`))
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL

	req := &core.ChargeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "",
		Capture:       true,
	}
	resp, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if resp.NextAction == nil {
		t.Fatal("NextAction is nil, want redirect")
	}
	if resp.NextAction.Type != core.NextActionRedirect {
		t.Errorf("NextAction.Type = %v, want %v", resp.NextAction.Type, core.NextActionRedirect)
	}
	if resp.NextAction.RedirectURL != "https://kushki.com/redirect/123" {
		t.Errorf("RedirectURL = %v, want https://kushki.com/redirect/123", resp.NextAction.RedirectURL)
	}
}

func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID: "test",
		Provider: Provider,
		Country:  "EC",
		Mode:     core.EnvTest,
		Secret:   "test_secret",
	}
	adapter, _ := New(tctx)
	adapter.(*Adapter).base = server.URL
	adapter.(*Adapter).hc.Timeout = 100 * time.Millisecond

	req := &core.ChargeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test123",
		Capture:       true,
	}
	_, err := adapter.Charge(context.Background(), req)
	if err == nil {
		t.Fatal("Charge() expected timeout error, got nil")
	}
}
