package adyen

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestBuildWebhookPayload(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		want    string
		wantErr bool
	}{
		{
			name: "full event with amount",
			body: []byte(`{
				"pspReference": "psp_123",
				"originalReference": "orig_123",
				"merchantAccountCode": "TestMerchant",
				"merchantReference": "order_456",
				"eventCode": "AUTHORISATION",
				"eventDate": 1234567890,
				"success": true,
				"amount": {
					"value": 10000,
					"currency": "USD"
				}
			}`),
			want:    "psp_123,orig_123,TestMerchant,order_456,10000,USD,AUTHORISATION,true",
			wantErr: false,
		},
		{
			name: "event without amount",
			body: []byte(`{
				"pspReference": "psp_456",
				"merchantReference": "order_789",
				"eventCode": "CANCELLATION",
				"eventDate": 1234567890,
				"success": true
			}`),
			want:    "psp_456,,,order_789,,CANCELLATION,true",
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
			got, err := buildWebhookPayload(tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildWebhookPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("buildWebhookPayload() = %v, want %v", got, tt.want)
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
			header:  "keyId=wsig_123,signature=abc123,algorithm=HMAC-SHA256",
			wantKey: "wsig_123",
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
			header:  "keyId=wsig_123,algorithm=HMAC-SHA256",
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
			key, sig, err := parseSignatureHeader(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSignatureHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if key != tt.wantKey {
					t.Errorf("parseSignatureHeader() key = %v, want %v", key, tt.wantKey)
				}
				if sig != tt.wantSig {
					t.Errorf("parseSignatureHeader() sig = %v, want %v", sig, tt.wantSig)
				}
			}
		})
	}
}

func TestAdyenVerifier_Verify(t *testing.T) {
	verifier := &adyenVerifier{}
	
	tests := []struct {
		name    string
		body    []byte
		headers http.Header
		secret  string
		wantErr bool
	}{
		{
			name: "valid signature",
			body: []byte(`{
				"pspReference": "psp_123",
				"merchantReference": "order_456",
				"eventCode": "AUTHORISATION",
				"success": true
			}`),
			headers: http.Header{
				"X-Adyen-Signature": []string{"keyId=wsig_123,signature=" + generateHMAC([]byte(`{
				"pspReference": "psp_123",
				"merchantReference": "order_456",
				"eventCode": "AUTHORISATION",
				"success": true
			}`), "test_secret")},
			},
			secret:  "test_secret",
			wantErr: false,
		},
		{
			name: "missing signature header",
			body: []byte(`{}`),
			headers: http.Header{},
			wantErr: true,
		},
		{
			name: "signature mismatch",
			body: []byte(`{}`),
			headers: http.Header{
				"X-Adyen-Signature": []string{"keyId=wsig_123,signature=wrong"},
			},
			secret:  "test_secret",
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
		want    string
		wantErr bool
	}{
		{
			name: "authorization event",
			body: []byte(`{
				"pspReference": "psp_123",
				"eventCode": "AUTHORISATION",
				"success": true,
				"merchantReference": "order_456"
			}`),
			want:    "payment.authorized",
			wantErr: false,
		},
		{
			name: "capture event",
			body: []byte(`{
				"pspReference": "psp_456",
				"eventCode": "CAPTURE",
				"success": true
			}`),
			want:    "payment.captured",
			wantErr: false,
		},
		{
			name: "refund event",
			body: []byte(`{
				"pspReference": "psp_789",
				"eventCode": "REFUND",
				"success": true
			}`),
			want:    "refund.completed",
			wantErr: false,
		},
		{
			name: "chargeback event",
			body: []byte(`{
				"pspReference": "psp_abc",
				"eventCode": "CHARGEBACK",
				"success": true
			}`),
			want:    "dispute.opened",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := normalizer.Normalize(tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && ev.Type != tt.want {
				t.Errorf("Normalize() type = %v, want %v", ev.Type, tt.want)
			}
		})
	}
}

// Helper function to generate HMAC for testing
func generateHMAC(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
