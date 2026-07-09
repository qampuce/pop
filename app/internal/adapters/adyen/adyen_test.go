package adyen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qampu/pop/internal/core"
)

func TestAdyenAdapter(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if adapter.Provider() != Provider {
		t.Errorf("Provider() = %v, want %v", adapter.Provider(), Provider)
	}

	caps := adapter.Capabilities()
	if !caps.SupportsAuthOnly {
		t.Error("Capabilities should support auth-only")
	}
	if !caps.SupportsRefundPartial {
		t.Error("Capabilities should support partial refunds")
	}
	if !caps.SupportsVaulting {
		t.Error("Capabilities should support vaulting")
	}
}

func TestAdyenAdapter_Tokenize(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		req     *core.TokenizeRequest
		wantErr bool
	}{
		{
			name: "valid card tokenize",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"token": "tok_adyen_123",
				})
			},
			req: &core.TokenizeRequest{
				Method: core.MethodCard,
				Card: &core.CardToken{
					Token: "tok_test_123",
					Last4: "4242",
					Brand: "visa",
				},
			},
			wantErr: false,
		},
		{
			name: "unsupported method",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"errorCode": "101",
					"message": "Invalid request",
				})
			},
			req: &core.TokenizeRequest{
				Method: core.MethodPix,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			tctx := &core.TenantContext{
				TenantID:      "test_tenant",
				Provider:      Provider,
				Country:       "US",
				Mode:          core.EnvTest,
				Secret:        "test_merchant",
				WebhookSecret: "test_api_key",
				EndpointURL:   server.URL,
			}

			adapter, err := New(tctx)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			_, err = adapter.Tokenize(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Tokenize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAdyenAdapter_Authorize(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_auth_123",
			"resultCode":    "Authorised",
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.AuthorizeRequest{
		Reference:     "test_auth_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
	}

	res, err := adapter.Authorize(context.Background(), req)
	if err != nil {
		t.Errorf("Authorize() error = %v", err)
	}
	if res == nil {
		t.Fatal("Authorize() returned nil result")
	}
	if res.Status != core.StatusAuthorized {
		t.Errorf("Authorize() status = %v, want %v", res.Status, core.StatusAuthorized)
	}
}

func TestAdyenAdapter_Charge(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_charge_123",
			"resultCode":    "Captured",
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.ChargeRequest{
		Reference:     "test_charge_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
		Capture:       true,
	}

	res, err := adapter.Charge(context.Background(), req)
	if err != nil {
		t.Errorf("Charge() error = %v", err)
	}
	if res == nil {
		t.Fatal("Charge() returned nil result")
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("Charge() status = %v, want %v", res.Status, core.StatusCaptured)
	}
}

func TestAdyenAdapter_Capture(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_capture_123",
			"response":     "[capture-received]",
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.CaptureRequest{
		AuthorizationID: "auth_123",
		Amount:          core.Money{Amount: 10000, Currency: "USD"},
	}

	res, err := adapter.Capture(context.Background(), req)
	if err != nil {
		t.Errorf("Capture() error = %v", err)
	}
	if res == nil {
		t.Fatal("Capture() returned nil result")
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("Capture() status = %v, want %v", res.Status, core.StatusCaptured)
	}
}

func TestAdyenAdapter_Void(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_void_123",
			"response":     "[cancel-received]",
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.VoidRequest{
		AuthorizationID: "auth_123",
		Reason:          "duplicate",
	}

	res, err := adapter.Void(context.Background(), req)
	if err != nil {
		t.Errorf("Void() error = %v", err)
	}
	if res == nil {
		t.Fatal("Void() returned nil result")
	}
	if res.Status != core.StatusVoided {
		t.Errorf("Void() status = %v, want %v", res.Status, core.StatusVoided)
	}
}

func TestAdyenAdapter_Refund(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_refund_123",
			"response":     "[refund-received]",
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.RefundRequest{
		PaymentID: "payment_123",
		Amount:    core.Money{Amount: 10000, Currency: "USD"},
		Reason:    core.RefundRequestedByCustomer,
	}

	res, err := adapter.Refund(context.Background(), req)
	if err != nil {
		t.Errorf("Refund() error = %v", err)
	}
	if res == nil {
		t.Fatal("Refund() returned nil result")
	}
	if res.Status != core.StatusRefunded {
		t.Errorf("Refund() status = %v, want %v", res.Status, core.StatusRefunded)
	}
}

func TestMapPaymentStatus(t *testing.T) {
	tests := []struct {
		code string
		want core.PaymentStatus
	}{
		{"Authorised", core.StatusAuthorized},
		{"Captured", core.StatusCaptured},
		{"Refunded", core.StatusRefunded},
		{"Cancelled", core.StatusVoided},
		{"Pending", core.StatusPending},
		{"Received", core.StatusPending},
		{"Unknown", core.StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := mapPaymentStatus(tt.code); got != tt.want {
				t.Errorf("mapPaymentStatus(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestParseAdyenError(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{
			name: "auth error",
			body: []byte(`{"errorCode": "901", "message": "Invalid API key", "status": 401}`),
			wantErr: true,
		},
		{
			name: "card declined",
			body: []byte(`{"errorCode": "103", "message": "Card declined", "status": 403}`),
			wantErr: true,
		},
		{
			name: "insufficient funds",
			body: []byte(`{"errorCode": "104", "message": "Insufficient funds", "status": 403}`),
			wantErr: true,
		},
		{
			name: "invalid json",
			body: []byte(`invalid json`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseAdyenError(tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseAdyenError() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNew_MissingMerchant(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "", // Missing merchant
		WebhookSecret: "test_api_key",
	}

	_, err := New(tctx)
	if err == nil {
		t.Error("New() should return error when merchant is missing")
	}
}

func TestAdyenAdapter_With3DS2(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_3ds_123",
			"resultCode":    "Authorised",
			"action": map[string]any{
				"type":  "threeDS2",
				"token": "three_ds_token_123",
			},
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.AuthorizeRequest{
		Reference:     "test_3ds_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
	}

	res, err := adapter.Authorize(context.Background(), req)
	if err != nil {
		t.Errorf("Authorize() error = %v", err)
	}
	if res == nil {
		t.Fatal("Authorize() returned nil result")
	}
	if res.NextAction == nil {
		t.Error("Authorize() should return NextAction for 3DS2")
	}
	if res.NextAction.Type != core.NextAction3DS {
		t.Errorf("NextAction.Type = %v, want %v", res.NextAction.Type, core.NextAction3DS)
	}
	if res.NextAction.Token3DS == "" {
		t.Error("NextAction.Token3DS should be set for 3DS2")
	}
}

func TestAdyenAdapter_WithRedirect(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"pspReference": "psp_redirect_123",
			"resultCode":    "Pending",
			"action": map[string]any{
				"type": "redirect",
				"url":  "https://example.com/redirect",
			},
		})
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
		EndpointURL:   server.URL,
	}

	adapter, err := New(tctx)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &core.AuthorizeRequest{
		Reference:     "test_redirect_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
	}

	res, err := adapter.Authorize(context.Background(), req)
	if err != nil {
		t.Errorf("Authorize() error = %v", err)
	}
	if res == nil {
		t.Fatal("Authorize() returned nil result")
	}
	if res.NextAction == nil {
		t.Error("Authorize() should return NextAction for redirect")
	}
	if res.NextAction.Type != core.NextActionRedirect {
		t.Errorf("NextAction.Type = %v, want %v", res.NextAction.Type, core.NextActionRedirect)
	}
	if res.NextAction.RedirectURL == "" {
		t.Error("NextAction.RedirectURL should be set for redirect")
	}
}
