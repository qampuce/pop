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

			err := verifier.Verify(req, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDLocalNormalizer_Normalize(t *testing.T) {
	normalizer := &dlocalNormalizer{}

	tests := []struct {
		name      string
		body      string
		wantType  string
		wantStatus core.PaymentStatus
		wantErr   bool
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
			wantType:  "payment.captured",
			wantStatus: core.StatusCaptured,
			wantErr:   false,
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
			wantType:  "payment.authorized",
			wantStatus: core.StatusAuthorized,
			wantErr:   false,
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
			wantType:  "payment.failed",
			wantStatus: core.StatusFailed,
			wantErr:   false,
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
			wantType:  "refund.completed",
			wantStatus: core.StatusRefunded,
			wantErr:   false,
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
				if evt.EventType != tt.wantType {
					t.Errorf("EventType = %v, want %v", evt.EventType, tt.wantType)
				}
				if evt.Status != tt.wantStatus {
					t.Errorf("Status = %v, want %v", evt.Status, tt.wantStatus)
				}
				if evt.Provider != Provider {
					t.Errorf("Provider = %v, want %v", evt.Provider, Provider)
				}
			}
		})
	}
}

func TestMapEventType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"payment.paid", "payment.captured"},
		{"payment.authorized", "payment.authorized"},
		{"payment.rejected", "payment.failed"},
		{"payment.cancelled", "payment.voided"},
		{"payment.pending", "payment.pending"},
		{"refund.success", "refund.completed"},
		{"refund.failed", "refund.failed"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapEventType(tt.input)
			if result != tt.expected {
				t.Errorf("mapEventType(%s) = %v, want %v", tt.input, result, tt.expected)
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
	err := verifier.Verify(req, secret)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	// Normalizar evento
	evt, err := normalizer.Normalize([]byte(body))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if evt.EventType != "payment.captured" {
		t.Errorf("EventType = %v, want payment.captured", evt.EventType)
	}
	if evt.Status != core.StatusCaptured {
		t.Errorf("Status = %v, want %v", evt.Status, core.StatusCaptured)
	}
	if evt.PaymentID != "pay_int" {
		t.Errorf("PaymentID = %v, want pay_int", evt.PaymentID)
	}
}
