package adyen

import (
	"context"
	"testing"
	"time"

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

	tests := []struct {
		name    string
		req     *core.TokenizeRequest
		wantErr bool
	}{
		{
			name: "valid card tokenize",
			req: &core.TokenizeRequest{
				Method: core.MethodCard,
				Card: &core.CardData{
					Token: "tok_test_123",
					Last4: "4242",
					Brand: "visa",
				},
			},
			wantErr: true, // Error de red en test sin mock
		},
		{
			name: "unsupported method",
			req: &core.TokenizeRequest{
				Method: core.MethodPix,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := adapter.Tokenize(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Tokenize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAdyenAdapter_Authorize(t *testing.T) {
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

	req := &core.AuthorizeRequest{
		Reference:     "test_auth_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
	}

	_, err = adapter.Authorize(context.Background(), req)
	if err == nil {
		t.Error("Authorize() should return error in test without mock")
	}
}

func TestAdyenAdapter_Charge(t *testing.T) {
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

	req := &core.ChargeRequest{
		Reference:     "test_charge_123",
		Amount:        core.Money{Amount: 10000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "tok_test_123",
		Capture:       true,
	}

	_, err = adapter.Charge(context.Background(), req)
	if err == nil {
		t.Error("Charge() should return error in test without mock")
	}
}

func TestAdyenAdapter_Capture(t *testing.T) {
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

	req := &core.CaptureRequest{
		AuthorizationID: "auth_123",
		Amount:          core.Money{Amount: 10000, Currency: "USD"},
	}

	_, err = adapter.Capture(context.Background(), req)
	if err == nil {
		t.Error("Capture() should return error in test without mock")
	}
}

func TestAdyenAdapter_Void(t *testing.T) {
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

	req := &core.VoidRequest{
		AuthorizationID: "auth_123",
		Reason:          "duplicate",
	}

	_, err = adapter.Void(context.Background(), req)
	if err == nil {
		t.Error("Void() should return error in test without mock")
	}
}

func TestAdyenAdapter_Refund(t *testing.T) {
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

	req := &core.RefundRequest{
		PaymentID: "payment_123",
		Amount:    core.Money{Amount: 10000, Currency: "USD"},
		Reason:    core.RefundRequestedByCustomer,
	}

	_, err = adapter.Refund(context.Background(), req)
	if err == nil {
		t.Error("Refund() should return error in test without mock")
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
