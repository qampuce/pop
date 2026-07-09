package niubiz

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/webhook"
)

// niubizVerifier verifica la firma HMAC-SHA256 de webhooks de Niubiz.
//
// Niubiz envía la firma en el header X-Signature. El formato es:
// X-Signature: hmac-sha256=HEX(timestamp + body)
type niubizVerifier struct{}

func (v *niubizVerifier) Verify(body []byte, headers http.Header, secret string) error {
	sigHeader := headers.Get("X-Signature")
	if sigHeader == "" {
		return fmt.Errorf("missing X-Signature header")
	}

	// Extraer timestamp y firma del header.
	// Formato: hmac-sha256=HEX(timestamp + body)
	if !strings.HasPrefix(sigHeader, "hmac-sha256=") {
		return fmt.Errorf("invalid signature format")
	}

	sig := strings.TrimPrefix(sigHeader, "hmac-sha256=")

	// Calcular firma esperada.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(time.Now().Unix(), 10)))
	mac.Write(body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// niubizNormalizer normaliza eventos de webhook de Niubiz al formato estándar.
type niubizNormalizer struct{}

func (n *niubizNormalizer) Normalize(body []byte) (*webhook.NormalizedEvent, error) {
	var env struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		CreatedAt int64           `json:"created_at"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("unmarshal webhook: %v", err)
	}

	// Mapear tipo de evento Niubiz → evento canónico.
	var eventType string
	switch env.Type {
	case "payment.success":
		eventType = "payment.captured"
	case "payment.authorized":
		eventType = "payment.authorized"
	case "payment.failed":
		eventType = "payment.failed"
	case "payment.voided":
		eventType = "payment.voided"
	case "refund.success":
		eventType = "refund.completed"
	case "refund.failed":
		eventType = "refund.failed"
	default:
		eventType = env.Type
	}

	// Construir payload canónico.
	payload := map[string]any{
		"provider": Provider,
		"id":        env.ID,
		"type":      env.Type,
		"createdAt": env.CreatedAt,
	}

	return &webhook.NormalizedEvent{
		Provider: Provider,
		Type:     eventType,
		Payload:  payload,
		Raw:      body,
	}, nil
}
