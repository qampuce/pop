// Package dlocal tests para el webhook handler dLocal.
package dlocal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

func TestDLocalVerifier_Verify(t *testing.T) {
	secret := "test_webhook_secret"
	verifier := &dlocalVerifier{}

	tests := []struct {
		name    string
		body    string
		date    string
		sign    string
		wantErr bool
	}{
		{
			name:    "valid signature",
			body:    `{"test": "data"}`,
			date:    time.Now().UTC().Format(time.RFC1123),
			wantErr: false,
		},
		{
			name:    "invalid signature",
			body:    `{"test": "data"}`,
			date:    time.Now().UTC().Format(time.RFC1123),
			sign:    "invalid_signature",
			wantErr: true,
		},
		{
			name:    "missing date header",
			body:    `{"test": "data"}`,
			wantErr: true,
		},
		{
			name:    "missing signature header",
			body:    `{"test": "data"}`,
			date:    time.Now().UTC().Format(time.RFC1123),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			date := tt.date
			sign := tt.sign

			// Si no se proporciona firma, generar la correcta
			if sign == "" && !tt.wantErr {
				sign = verifier.sign(date, body, secret)
			}

			req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
			if date != "" {
				req.Header.Set("X-Date", date)
			}
			if sign != "" {
				req.Header.Set("X-Signature", sign)
			}

			err := verifier.Verify(body, req.Header, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDLocalNormalizer_Normalize(t *testing.T) {
	normalizer := &dlocalNormalizer{}

	tests := []struct {
		name     string
		body     string
		wantType string
		wantErr  bool
	}{
		{
			name: "payment paid",
			body: `{
				"id": "evt_123",
				"type": "payment.paid",
				"created_at": "2025-01-02T15:04:05Z",
				"data": {
					"id": "pay_123",
					"status": "PAID",
					"amount": 100.00,
					"currency": "USD"
				}
			}`,
			wantType: "payment.captured",
			wantErr:  false,
		},
		{
			name: "payment authorized",
			body: `{
				"id": "evt_456",
				"type": "payment.authorized",
				"created_at": "2025-01-02T15:04:05Z",
				"data": {
					"id": "pay_456",
					"status": "AUTHORIZED",
					"amount": 50.00,
					"currency": "USD"
				}
			}`,
			wantType: "payment.authorized",
			wantErr:  false,
		},
		{
			name: "payment rejected",
			body: `{
				"id": "evt_789",
				"type": "payment.rejected",
				"created_at": "2025-01-02T15:04:05Z",
				"data": {
					"id": "pay_789",
					"status": "REJECTED",
					"amount": 75.00,
					"currency": "USD"
				}
			}`,
			wantType: "payment.failed",
			wantErr:  false,
		},
		{
			name: "refund success",
			body: `{
				"id": "evt_ref",
				"type": "refund.success",
				"created_at": "2025-01-02T15:04:05Z",
				"data": {
					"id": "pay_ref",
					"status": "REFUNDED",
					"amount": 25.00,
					"currency": "USD"
				}
			}`,
			wantType: "refund.completed",
			wantErr:  false,
		},
		{
			name:    "invalid json",
			body:    `invalid json`,
			wantErr: true,
		},
		{
			name: "unknown event type",
			body: `{
				"id": "evt_unk",
				"type": "unknown.type",
				"created_at": "2025-01-02T15:04:05Z"
			}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt, err := normalizer.Normalize([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if evt.Type != tt.wantType {
					t.Errorf("Type = %v, want %v", evt.Type, tt.wantType)
				}
				if evt.Provider != Provider {
					t.Errorf("Provider = %v, want %v", evt.Provider, Provider)
				}
			}
		})
	}
}

func TestSign(t *testing.T) {
	verifier := &dlocalVerifier{}
	secret := "test_secret"
	date := "Thu, 02 Jan 2025 15:04:05 UTC"
	body := []byte(`{"test": "data"}`)

	signature := verifier.sign(date, body, secret)
	if signature == "" {
		t.Error("sign() should return non-empty signature")
	}

	// Verificar que la firma es determinista
	signature2 := verifier.sign(date, body, secret)
	if signature != signature2 {
		t.Error("sign() should be deterministic")
	}

	// Verificar que diferentes inputs generan diferentes firmas
	signature3 := verifier.sign(date, []byte(`{"test": "different"}`), secret)
	if signature == signature3 {
		t.Error("sign() should generate different signatures for different inputs")
	}
}

func TestWebhookIntegration(t *testing.T) {
	// Test de integración completo: verificación + normalización
	secret := "test_webhook_secret"
	verifier := &dlocalVerifier{}
	normalizer := &dlocalNormalizer{}

	body := `{
		"id": "evt_int",
		"type": "payment.paid",
		"created_at": "2025-01-02T15:04:05Z",
		"data": {
			"id": "pay_int",
			"status": "PAID",
			"amount": 100.00,
			"currency": "USD"
		}
	}`

	date := time.Now().UTC().Format(time.RFC1123)
	signature := verifier.sign(date, []byte(body), secret)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte(body)))
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Signature", signature)

	// Verificar firma
	err := verifier.Verify([]byte(body), req.Header, secret)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	// Normalizar evento
	evt, err := normalizer.Normalize([]byte(body))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if evt.Type != "payment.captured" {
		t.Errorf("Type = %v, want payment.captured", evt.Type)
	}
	if evt.Provider != Provider {
		t.Errorf("Provider = %v, want %v", evt.Provider, Provider)
	}
}
