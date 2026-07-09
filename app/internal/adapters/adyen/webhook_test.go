package adyen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/qampu/pop/internal/core"
)

func TestAdyenVerifier(t *testing.T) {
	secret := "test_webhook_secret"
	body := []byte(`{"pspReference":"test123","eventCode":"AUTHORISATION"}`)

	// Generar firma válida
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	sigHeader := "keyId=wsig_test,signature=" + sig + ",algorithm=HMAC-SHA256"

	verifier := &adyenVerifier{}

	tests := []struct {
		name    string
		headers http.Header
		body    []byte
		wantErr bool
	}{
		{
			name: "valid signature",
			headers: http.Header{
				"X-Adyen-Signature":     []string{sigHeader},
				"X-Adyen-Webhook-Secret": []string{secret},
				"X-Adyen-Tenant":        []string{"test_tenant"},
			},
			body:    body,
			wantErr: false,
		},
		{
			name: "missing signature header",
			headers: http.Header{
				"X-Adyen-Webhook-Secret": []string{secret},
			},
			body:    body,
			wantErr: true,
		},
		{
			name: "missing secret header",
			headers: http.Header{
				"X-Adyen-Signature": []string{sigHeader},
			},
			body:    body,
			wantErr: true,
		},
		{
			name: "invalid signature",
			headers: http.Header{
				"X-Adyen-Signature":     []string{"keyId=wsig_test,signature=invalid,algorithm=HMAC-SHA256"},
				"X-Adyen-Webhook-Secret": []string{secret},
			},
			body:    body,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenantID, err := verifier.Verify(context.Background(), tt.headers, tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tenantID != "test_tenant" {
				t.Errorf("Verify() tenantID = %v, want test_tenant", tenantID)
			}
		})
	}
}

func TestAdyenNormalizer(t *testing.T) {
	tctx := &core.TenantContext{
		TenantID:      "test_tenant",
		Provider:      Provider,
		Country:       "US",
		Mode:          core.EnvTest,
		Secret:        "test_merchant",
		WebhookSecret: "test_api_key",
	}

	normalizer := &adyenNormalizer{}

	tests := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{
			name: "valid authorization event",
			body: []byte(`{
				"pspReference": "test123",
				"eventCode": "AUTHORISATION",
				"eventDate": 1620000000000,
				"success": true,
				"merchantReference": "order_123",
				"amount": {
					"value": 10000,
					"currency": "USD"
				}
			}`),
			wantErr: false,
		},
		{
			name: "valid capture event",
			body: []byte(`{
				"pspReference": "test456",
				"eventCode": "CAPTURE",
				"eventDate": 1620000000000,
				"success": true,
				"merchantReference": "order_123",
				"amount": {
					"value": 10000,
					"currency": "USD"
				}
			}`),
			wantErr: false,
		},
		{
			name: "valid refund event",
			body: []byte(`{
				"pspReference": "test789",
				"eventCode": "REFUND",
				"eventDate": 1620000000000,
				"success": true,
				"merchantReference": "order_123",
				"amount": {
					"value": 10000,
					"currency": "USD"
				}
			}`),
			wantErr: false,
		},
		{
			name:    "invalid json",
			body:    []byte(`invalid json`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := normalizer.Normalize(context.Background(), tctx, tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if ev.Provider != Provider {
					t.Errorf("Normalize() Provider = %v, want %v", ev.Provider, Provider)
				}
				if ev.TenantID != tctx.TenantID {
					t.Errorf("Normalize() TenantID = %v, want %v", ev.TenantID, tctx.TenantID)
				}
			}
		})
	}
}

func TestMapEventType(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"AUTHORISATION", "payment.authorized"},
		{"CAPTURE", "payment.captured"},
		{"CANCELLATION", "payment.voided"},
		{"REFUND", "refund.completed"},
		{"REFUND_FAILED", "refund.failed"},
		{"CHARGEBACK", "dispute.opened"},
		{"CHARGEBACK_REVERSED", "dispute.resolved"},
		{"PENDING", "payment.pending"},
		{"AUTHORISATION_FAILED", "payment.failed"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := mapEventType(tt.code)
			if got != tt.want {
				t.Errorf("mapEventType(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestMapPaymentStatus_Webhook(t *testing.T) {
	tests := []struct {
		success  bool
		eventCode string
		want     core.PaymentStatus
	}{
		{true, "AUTHORISATION", core.StatusAuthorized},
		{true, "CAPTURE", core.StatusCaptured},
		{true, "CANCELLATION", core.StatusVoided},
		{true, "REFUND", core.StatusRefunded},
		{true, "PENDING", core.StatusPending},
		{false, "AUTHORISATION", core.StatusFailed},
		{false, "CAPTURE", core.StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.eventCode, func(t *testing.T) {
			got := mapPaymentStatus(tt.success, tt.eventCode)
			if got != tt.want {
				t.Errorf("mapPaymentStatus(%v, %q) = %v, want %v", tt.success, tt.eventCode, got, tt.want)
			}
		})
	}
}

func TestParseSignatureHeader(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		wantKey string
		wantSig string
		wantErr bool
	}{
		{
			name:    "valid header",
			header:  "keyId=wsig_test,signature=abc123,algorithm=HMAC-SHA256",
			wantKey: "wsig_test",
			wantSig: "abc123",
			wantErr: false,
		},
		{
			name:    "missing keyId",
			header:  "signature=abc123,algorithm=HMAC-SHA256",
			wantErr: true,
		},
		{
			name:    "missing signature",
			header:  "keyId=wsig_test,algorithm=HMAC-SHA256",
			wantErr: true,
		},
		{
			name:    "empty header",
			header:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyID, sig, err := parseSignatureHeader(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSignatureHeader() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if keyID != tt.wantKey {
					t.Errorf("parseSignatureHeader() keyID = %v, want %v", keyID, tt.wantKey)
				}
				if sig != tt.wantSig {
					t.Errorf("parseSignatureHeader() sig = %v, want %v", sig, tt.wantSig)
				}
			}
		})
	}
}
