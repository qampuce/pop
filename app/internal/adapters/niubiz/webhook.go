package niubiz

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/webhook"
)

// niubizVerifier verifica la firma HMAC-SHA256 de webhooks de Niubiz.
//
// Niubiz envía la firma en el header X-Signature. El formato es:
// X-Signature: hmac-sha256=HEX(timestamp + body)
type niubizVerifier struct{}

func (v *niubizVerifier) Verify(ctx context.Context, headers http.Header, body []byte) (string, error) {
	secret := headers.Get("X-Niubiz-Webhook-Secret")
	if secret == "" {
		return "", core.NewError(core.ErrWebhookSignature, core.CategoryGateway, Provider,
			"missing X-Niubiz-Webhook-Secret header")
	}

	sigHeader := headers.Get("X-Signature")
	if sigHeader == "" {
		return "", core.NewError(core.ErrWebhookSignature, core.CategoryGateway, Provider,
			"missing X-Signature header")
	}

	// Extraer timestamp y firma del header.
	// Formato: hmac-sha256=HEX(timestamp + body)
	if !strings.HasPrefix(sigHeader, "hmac-sha256=") {
		return "", core.NewError(core.ErrWebhookSignature, core.CategoryGateway, Provider,
			"invalid signature format")
	}

	sig := strings.TrimPrefix(sigHeader, "hmac-sha256=")

	// Calcular firma esperada.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(time.Now().Unix(), 10)))
	mac.Write(body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", core.NewError(core.ErrWebhookSignature, core.CategoryGateway, Provider,
			"signature mismatch")
	}

	tenantID := headers.Get("X-Niubiz-Tenant")
	if tenantID == "" {
		return "", core.NewError(core.ErrWebhookSignature, core.CategoryGateway, Provider,
			"missing X-Niubiz-Tenant header")
	}

	return tenantID, nil
}

// niubizNormalizer normaliza eventos de webhook de Niubiz al formato estándar.
type niubizNormalizer struct{}

func (n *niubizNormalizer) Normalize(ctx context.Context, tctx *core.TenantContext, body []byte) (*webhook.Event, error) {
	var env struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		CreatedAt int64           `json:"created_at"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
			fmt.Sprintf("unmarshal webhook: %v", err))
	}

	// Parsear el payload según el tipo de evento.
	var evType webhook.EventType
	var status core.PaymentStatus
	var paymentID string
	var amount core.Money
	var reference string

	switch env.Type {
	case "payment.success":
		evType = webhook.EventPaymentCaptured
		status = core.StatusCaptured
		var pay struct {
			ID        string  `json:"id"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			OrderID   string  `json:"order_id"`
			CreatedAt int64   `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &pay); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal payment data: %v", err))
		}
		paymentID = pay.ID
		amount = core.Money{Amount: int64(pay.Amount * 100), Currency: pay.Currency}
		reference = pay.OrderID

	case "payment.authorized":
		evType = webhook.EventPaymentAuthorized
		status = core.StatusAuthorized
		var pay struct {
			ID        string  `json:"id"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			OrderID   string  `json:"order_id"`
			CreatedAt int64  `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &pay); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal payment data: %v", err))
		}
		paymentID = pay.ID
		amount = core.Money{Amount: int64(pay.Amount * 100), Currency: pay.Currency}
		reference = pay.OrderID

	case "payment.failed":
		evType = webhook.EventPaymentFailed
		status = core.StatusFailed
		var pay struct {
			ID        string  `json:"id"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			OrderID   string  `json:"order_id"`
			CreatedAt int64  `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &pay); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal payment data: %v", err))
		}
		paymentID = pay.ID
		amount = core.Money{Amount: int64(pay.Amount * 100), Currency: pay.Currency}
		reference = pay.OrderID

	case "payment.voided":
		evType = webhook.EventPaymentVoided
		status = core.StatusVoided
		var pay struct {
			ID        string  `json:"id"`
			OrderID   string  `json:"order_id"`
			CreatedAt int64  `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &pay); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal payment data: %v", err))
		}
		paymentID = pay.ID
		reference = pay.OrderID

	case "refund.success":
		evType = webhook.EventRefundCompleted
		status = core.StatusRefunded
		var ref struct {
			ID        string  `json:"id"`
			PaymentID string  `json:"payment_id"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			CreatedAt int64  `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &ref); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal refund data: %v", err))
		}
		paymentID = ref.PaymentID
		amount = core.Money{Amount: int64(ref.Amount * 100), Currency: ref.Currency}
		reference = ref.PaymentID

	case "refund.failed":
		evType = webhook.EventRefundFailed
		status = core.StatusFailed
		var ref struct {
			ID        string  `json:"id"`
			PaymentID string  `json:"payment_id"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			CreatedAt int64  `json:"created_at"`
		}
		if err := json.Unmarshal(env.Data, &ref); err != nil {
			return nil, core.NewError(core.ErrWebhookParse, core.CategoryGateway, Provider,
				fmt.Sprintf("unmarshal refund data: %v", err))
		}
		paymentID = ref.PaymentID
		amount = core.Money{Amount: int64(ref.Amount * 100), Currency: ref.Currency}
		reference = ref.PaymentID

	default:
		// Evento desconocido: pasar como pending para auditoría.
		evType = webhook.EventPaymentPending
		status = core.StatusPending
	}

	return &webhook.Event{
		Type:            evType,
		ProviderEventID: env.ID,
		PaymentID:       paymentID,
		Status:          status,
		Amount:          amount,
		Provider:        Provider,
		TenantID:        tctx.TenantID,
		Country:         tctx.Country,
		Reference:       reference,
		CreatedAt:        time.Unix(env.CreatedAt, 0).UTC(),
		Raw:             body,
	}, nil
}
