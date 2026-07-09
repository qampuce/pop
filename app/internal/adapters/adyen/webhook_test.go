package adyen

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
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
		secret  string
		wantErr bool
	}{
		{
			name: "valid signature",
			headers: http.Header{
				"X-Adyen-Signature": []string{sigHeader},
			},
			body:    body,
			secret:  secret,
			wantErr: false,
		},
		{
			name: "missing signature header",
			headers: http.Header{
				"X-Adyen-Webhook-Secret": []string{secret},
			},
			body:    body,
			secret:  secret,
			wantErr: true,
		},
		{
			name: "missing secret header",
			headers: http.Header{
				"X-Adyen-Signature": []string{sigHeader},
			},
			body:    body,
			secret:  secret,
			wantErr: true,
		},
		{
			name: "invalid signature",
			headers: http.Header{
				"X-Adyen-Signature": []string{"keyId=wsig_test,signature=invalid,algorithm=HMAC-SHA256"},
			},
			body:    body,
			secret:  secret,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifier.Verify(tt.body, tt.headers, tt.secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("Verify() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAdyenNormalizer(t *testing.T) {
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
			ev, err := normalizer.Normalize(tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if ev.Provider != Provider {
					t.Errorf("Normalize() Provider = %v, want %v", ev.Provider, Provider)
				}
			}
		})
	}
}
