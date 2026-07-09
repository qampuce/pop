// Package dlocal tests para el adapter dLocal.
package dlocal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
)

func TestAdapter_Capabilities(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if adapter.Provider() != Provider {
		t.Errorf("Provider() = %v, want %v", adapter.Provider(), Provider)
	}

	caps := adapter.Capabilities()
	if len(caps.Countries) == 0 {
		t.Error("Capabilities should include countries")
	}
	if len(caps.Currencies) == 0 {
		t.Error("Capabilities should include currencies")
	}
	if len(caps.Methods) == 0 {
		t.Error("Capabilities should include methods")
	}
	if !caps.SupportsAuthOnly {
		t.Error("dLocal should support auth-only")
	}
	if !caps.SupportsRefundPartial {
		t.Error("dLocal should support partial refunds")
	}
	if !caps.SupportsVaulting {
		t.Error("dLocal should support vaulting")
	}
}

func TestAdapter_Charge(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verificar headers de autenticación
		if r.Header.Get("X-Login") != "test_secret" {
			t.Errorf("missing X-Login header")
		}
		if r.Header.Get("X-Date") == "" {
			t.Errorf("missing X-Date header")
		}
		if r.Header.Get("X-Signature") == "" {
			t.Errorf("missing X-Signature header")
		}

		// Responder con payment exitoso
		response := map[string]any{
			"id":         "pay_123",
			"status":     "PAID",
			"amount":     100.00,
			"currency":   "USD",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.ChargeRequest{
		Reference:     "order_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_123",
		Capture:       true,
	}

	res, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}

	if res.Status != core.StatusCaptured {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusCaptured)
	}
	if res.ID != "pay_123" {
		t.Errorf("ID = %v, want pay_123", res.ID)
	}
	if res.Provider != Provider {
		t.Errorf("Provider = %v, want %v", res.Provider, Provider)
	}
}

func TestAdapter_Authorize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"id":         "pay_456",
			"status":     "AUTHORIZED",
			"amount":     50.00,
			"currency":   "USD",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.AuthorizeRequest{
		Reference:     "order_456",
		Amount:        core.Money{Amount: 5000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_456",
	}

	res, err := adapter.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}

	if res.Status != core.StatusAuthorized {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusAuthorized)
	}
	if res.ID != "pay_456" {
		t.Errorf("ID = %v, want pay_456", res.ID)
	}
}

func TestAdapter_Capture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"id":         "pay_789",
			"status":     "PAID",
			"amount":     50.00,
			"currency":   "USD",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.CaptureRequest{
		AuthorizationID: "pay_789",
		Amount:         core.Money{Amount: 5000, Currency: "USD"},
	}

	res, err := adapter.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}

	if res.Status != core.StatusCaptured {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusCaptured)
	}
}

func TestAdapter_Refund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"id":     "ref_123",
			"status": "SUCCESS",
			"amount": 50.00,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.RefundRequest{
		PaymentID: "pay_123",
		Amount:    core.Money{Amount: 5000, Currency: "USD"},
		Reason:    core.RefundRequestedByCustomer,
	}

	res, err := adapter.Refund(context.Background(), req)
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}

	if res.Status != core.StatusRefunded {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusRefunded)
	}
	if res.ID != "ref_123" {
		t.Errorf("ID = %v, want ref_123", res.ID)
	}
}

func TestAdapter_Void(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"id":         "pay_999",
			"status":     "CANCELLED",
			"amount":     0.00,
			"currency":   "USD",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.VoidRequest{
		AuthorizationID: "pay_999",
		Reason:          "customer_request",
	}

	res, err := adapter.Void(context.Background(), req)
	if err != nil {
		t.Fatalf("Void() error = %v", err)
	}

	if res.Status != core.StatusVoided {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusVoided)
	}
}

func TestAdapter_Tokenize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"id": "tok_456",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.TokenizeRequest{
		Method: core.MethodCard,
		Card: &core.CardToken{
			Token:  "frontend_token",
			Last4:  "4242",
			Brand:  "visa",
		},
	}

	res, err := adapter.Tokenize(context.Background(), req)
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}

	if res.ProviderToken != "tok_456" {
		t.Errorf("ProviderToken = %v, want tok_456", res.ProviderToken)
	}
	if !res.Vaulted {
		t.Error("Vaulted should be true")
	}
}

func TestAdapter_Tokenize_UnsupportedMethod(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "AR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.TokenizeRequest{
		Method: core.MethodPix, // APM no soportado para tokenize
	}

	_, err = adapter.Tokenize(context.Background(), req)
	if err == nil {
		t.Error("Tokenize() should return error for unsupported method")
	}
}

func TestAdapter_APM_Charge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verificar que se envió payment_method_id correcto
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["payment_method_id"] != "PX" {
			t.Errorf("payment_method_id = %v, want PX", payload["payment_method_id"])
		}

		response := map[string]any{
			"id":          "pay_pix",
			"status":      "PENDING",
			"amount":      100.00,
			"currency":    "BRL",
			"created_at":  time.Now().UTC().Format(time.RFC3339),
			"redirect_url": "https://example.com/redirect",
			"qr_code":     "base64_qr_code",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test",
		Provider:      Provider,
		Country:       "BR",
		Mode:          core.EnvTest,
		Secret:        "test_secret",
		WebhookSecret: "test_webhook_secret",
		EndpointURL:   server.URL,
	}
	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.ChargeRequest{
		Reference: "order_pix",
		Amount:    core.Money{Amount: 10000, Currency: "BRL"},
		Method:    core.MethodPix,
		Capture:   true,
	}

	res, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}

	if res.Status != core.StatusPending {
		t.Errorf("Status = %v, want %v", res.Status, core.StatusPending)
	}
	if res.NextAction == nil {
		t.Error("NextAction should be present for APM")
	}
	if res.NextAction.Type != core.NextActionQR {
		t.Errorf("NextAction.Type = %v, want %v", res.NextAction.Type, core.NextActionQR)
	}
}

func TestMapPaymentStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected core.PaymentStatus
	}{
		{"PAID", core.StatusCaptured},
		{"AUTHORIZED", core.StatusAuthorized},
		{"PENDING", core.StatusPending},
		{"REJECTED", core.StatusFailed},
		{"CANCELLED", core.StatusVoided},
		{"REFUNDED", core.StatusRefunded},
		{"PARTIALLY_REFUNDED", core.StatusPartiallyRefunded},
		{"UNKNOWN", core.StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapPaymentStatus(tt.input)
			if result != tt.expected {
				t.Errorf("mapPaymentStatus(%s) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseDLocalError(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		checkErr func(t *testing.T, err error)
	}{
		{
			name: "auth error",
			body: `{"code": "4001", "message": "invalid credentials"}`,
			checkErr: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("expected error")
				}
				nerr, ok := err.(*core.NormalizedError)
				if !ok {
					t.Fatal("expected NormalizedError")
				}
				if nerr.Code != core.ErrInvalidCredentials {
					t.Errorf("Code = %v, want %v", nerr.Code, core.ErrInvalidCredentials)
				}
			},
		},
		{
			name: "card declined",
			body: `{"code": "5001", "message": "card declined"}`,
			checkErr: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("expected error")
				}
				nerr, ok := err.(*core.NormalizedError)
				if !ok {
					t.Fatal("expected NormalizedError")
				}
				if nerr.Code != core.ErrCardDeclined {
					t.Errorf("Code = %v, want %v", nerr.Code, core.ErrCardDeclined)
				}
			},
		},
		{
			name: "insufficient funds",
			body: `{"code": "5002", "message": "insufficient funds"}`,
			checkErr: func(t *testing.T, err error) {
				if err == nil {
					t.Fatal("expected error")
				}
				nerr, ok := err.(*core.NormalizedError)
				if !ok {
					t.Fatal("expected NormalizedError")
				}
				if nerr.Code != core.ErrInsufficientFunds {
					t.Errorf("Code = %v, want %v", nerr.Code, core.ErrInsufficientFunds)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseDLocalError([]byte(tt.body))
			tt.checkErr(t, err)
		})
	}
}
