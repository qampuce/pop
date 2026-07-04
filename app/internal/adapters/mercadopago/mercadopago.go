// Package mercadopago es el adapter real para la pasarela Mercado Pago.
//
// Implementa core.Gateway invocando a la REST API de Mercado Pago (v1) usando
// únicamente la stdlib de Go (sin SDK externo). Mantiene el módulo sin
// dependencias adicionales y el contrato explícito con el proveedor.
//
// Mercado Pago usa autenticación Bearer con el access_token del integrador.
// Los montos viajan en la unidad mínima de la moneda (cents para la mayoría
// de las monedas Latam) — coincide con el formato interno del SDK
// (core.Money.Amount), así que no hay conversión.
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/mercadopago"
//
// Esto pobla factory.Default y webhook.Default en init().
package mercadopago

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/webhook"
)

// Provider es el ProviderID canónico de Mercado Pago.
const Provider core.ProviderID = core.ProviderMercadoPago

// Caps describe las capabilities estáticas de Mercado Pago para el router.
// MP está disponible en toda Latam + EE.UU., soporta auth-only, refunds
// parciales y vaulting (customers + cards).
var Caps = core.Capabilities{
	Countries: []string{
		"AR", "BR", "CL", "CO", "MX", "PE", "UY", "EC", "VE",
		"US", "CR", "DO", "PA", "PY", "BO",
	},
	Currencies: []string{
		"ARS", "BRL", "CLP", "COP", "MXN", "PEN", "UYU", "USD",
		"CRC", "DOP", "PAB", "PYG", "BOB", "VES",
	},
	Methods: []core.PaymentMethod{
		core.MethodCard,
		core.MethodPix,
		core.MethodSPEI,
		core.MethodPSE,
		core.MethodPagoEfectivo,
	},
	SupportsAuthOnly:      true,
	SupportsRefundPartial: true,
	SupportsVaulting:      true,
}

// Base URLs (override vía TenantContext.EndpointURL para proxies/sandbox).
const (
	defaultBaseURL = "https://api.mercadopago.com"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(&webhook.WebhookHandler{
		Provider:  Provider,
		Verifier:  &mpVerifier{},
		Normalize: &mpNormalizer{},
	})
}

// Adapter implementa core.Gateway contra Mercado Pago.
type Adapter struct {
	tctx *core.TenantContext
	base string
	hc   *http.Client
}

// New construye un adapter Mercado Pago para el TenantContext dado.
func New(tctx *core.TenantContext) (core.Gateway, error) {
	base := strings.TrimRight(tctx.EndpointURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Adapter{
		tctx: tctx,
		base: base,
		hc:   &http.Client{Timeout: defaultTimeout},
	}, nil
}

func (a *Adapter) Provider() core.ProviderID       { return Provider }
func (a *Adapter) Capabilities() core.Capabilities { return Caps }

// --- helpers HTTP ---

// doJSON envía una request JSON a Mercado Pago con auth Bearer (access_token).
// method/path definen el endpoint; body es el payload JSON (puede ser nil).
// Agrega Idempotency-Key cuando op+ref están disponibles para reintentos
// seguros. Devuelve el body decodificado en `out` o un *core.NormalizedError.
func (a *Adapter) doJSON(ctx context.Context, method, op, ref, path string, body any, out any) error {
	endpoint := a.base + path

	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return core.NewError(core.ErrInternal, core.CategoryInternal, Provider,
				fmt.Sprintf("marshal body: %v", err))
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return core.NewError(core.ErrInternal, core.CategoryInternal, Provider,
			fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Authorization", "Bearer "+a.tctx.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if op != "" {
		req.Header.Set("Idempotency-Key", a.tctx.IdempotencyKey(op, ref))
	}

	resp, err := a.hc.Do(req)
	if err != nil {
		return core.NewTransient(core.ErrNetworkError, Provider,
			fmt.Sprintf("http call: %v", err), err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewTransient(core.ErrNetworkError, Provider,
			fmt.Sprintf("read body: %v", err), err)
	}

	if resp.StatusCode >= 500 {
		return core.NewTransient(core.ErrProviderDown, Provider,
			fmt.Sprintf("mercadopago %d: %s", resp.StatusCode, truncate(respBody)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider,
			"mercadopago rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseMPError(respBody)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return core.NewError(core.ErrInternal, core.CategoryInternal, Provider,
				fmt.Sprintf("decode response: %v", err))
		}
	}
	return nil
}

func truncate(b []byte) string {
	const max = 512
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// --- Operaciones Gateway ---

// Tokenize crea un card_token en Mercado Pago a partir del token del frontend.
// MP permite tokenizar en el frontend (SDK JS) y reusar el token; este método
// re-tokeniza desde el backend cuando el tenant necesita vaulting explícito.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"mercadopago tokenize only supports card method")
	}

	payload := map[string]any{
		"card_id":     in.Card.Token,
		"security_code": map[string]string{"code": "123"},
	}
	if in.Buyer != nil && in.Buyer.Email != "" {
		// MP no requiere payer al tokenizar, pero lo acepta para contexto.
	}

	var tok mpCardToken
	if err := a.doJSON(ctx, http.MethodPost, "tokenize", in.Card.Token, "/v1/card_tokens", payload, &tok); err != nil {
		return nil, err
	}
	return &core.TokenizeResponse{
		ProviderToken: tok.ID,
		Vaulted:       true,
		Method:        core.MethodCard,
		Last4:         in.Card.Last4,
		Brand:         in.Card.Brand,
	}, nil
}

// Authorize crea un payment con capture=false (auth-only).
func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	payload := a.paymentPayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, false)
	var pay mpPayment
	if err := a.doJSON(ctx, http.MethodPost, "authorize", in.Reference, "/v1/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Charge crea un payment con capture=true (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	payload := a.paymentPayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, in.Capture)
	var pay mpPayment
	if err := a.doJSON(ctx, http.MethodPost, "charge", in.Reference, "/v1/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un payment previamente autorizado.
// MP usa PUT /v1/payments/:id con capture=true.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	payload := map[string]any{
		"capture":           true,
		"transaction_amount": float64(in.Amount.Amount) / 100.0,
	}
	if in.Amount.Amount > 0 {
		// MP permite captura parcial via transaction_amount menor al autorizado.
	}
	var pay mpPayment
	path := "/v1/payments/" + in.AuthorizationID
	if err := a.doJSON(ctx, http.MethodPut, "capture", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, in.Amount, core.MethodCard), nil
}

// Void cancela un payment que está en state authorized/pendiente.
// MP usa PUT /v1/payments/:id con status="cancelled".
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	payload := map[string]any{"status": "cancelled"}
	if in.Reason != "" {
		payload["metadata"] = map[string]string{"void_reason": in.Reason}
	}
	var pay mpPayment
	path := "/v1/payments/" + in.AuthorizationID
	if err := a.doJSON(ctx, http.MethodPut, "void", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, core.Money{}, core.MethodCard), nil
}

// Refund emite un reembolso total o parcial sobre un payment.
// MP usa POST /v1/payments/:id/refunds con amount opcional (parcial).
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	payload := map[string]any{}
	if in.Amount.Amount > 0 {
		payload["amount"] = float64(in.Amount.Amount) / 100.0
	}
	var r mpRefund
	path := "/v1/payments/" + in.PaymentID + "/refunds"
	if err := a.doJSON(ctx, http.MethodPost, "refund", in.PaymentID, path, payload, &r); err != nil {
		return nil, err
	}
	return &core.RefundResult{
		ID:        strconv.FormatInt(r.ID, 10),
		PaymentID: in.PaymentID,
		Status:    mapRefundStatus(r.Status),
		Amount:    core.Money{Amount: int64(r.Amount * 100), Currency: in.Amount.Currency},
		Provider:  Provider,
		TenantID:  a.tctx.TenantID,
		Reference: in.PaymentID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// --- builders ---

func (a *Adapter) paymentPayload(
	ref string, amount core.Money, method core.PaymentMethod, token string,
	buyer *core.Buyer, desc string, meta map[string]string, capture bool,
) map[string]any {
	out := map[string]any{
		"transaction_amount": float64(amount.Amount) / 100.0,
		"currency_id":        strings.ToUpper(amount.Currency),
		"capture":            capture,
		"statement_descriptor": ref,
		"metadata":           mergeMeta(meta, map[string]string{"reference": ref}),
	}
	if desc != "" {
		out["description"] = desc
	}
	// Token de tarjeta del frontend (card) o datos APM.
	if token != "" {
		out["token"] = token
		out["payment_method_id"] = "default"
	}
	// APMs: Pix/SPEI/PSE/PagoEfectivo se seleccionan vía payment_method_id.
	switch method {
	case core.MethodPix:
		out["payment_method_id"] = "pix"
		delete(out, "token")
	case core.MethodSPEI:
		out["payment_method_id"] = "spei"
		delete(out, "token")
	case core.MethodPSE:
		out["payment_method_id"] = "pse"
		delete(out, "token")
	case core.MethodPagoEfectivo:
		out["payment_method_id"] = "pagoefectivo"
		delete(out, "token")
	}
	if buyer != nil {
		p := map[string]any{}
		if buyer.Email != "" {
			p["email"] = buyer.Email
		}
		if buyer.FirstName != "" || buyer.LastName != "" {
			p["first_name"] = buyer.FirstName
			p["last_name"] = buyer.LastName
		}
		if buyer.DocType != "" {
			p["identification"] = map[string]string{
				"type":   buyer.DocType,
				"number": buyer.DocNumber,
			}
		}
		out["payer"] = p
	}
	return out
}

func mergeMeta(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func (a *Adapter) toResult(pay *mpPayment, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        strconv.FormatInt(pay.ID, 10),
		Status:    mapPaymentStatus(pay.Status),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: parseMPDate(pay.DateCreated),
		Raw:       pay.Raw,
	}
	if pay.TransactionDetails != nil && pay.TransactionDetails.ExternalResourceURL != "" {
		// APMs (PSE, PagoEfectivo) generan un redirect URL.
		res.NextAction = &core.NextAction{
			Type:        core.NextActionRedirect,
			RedirectURL: pay.TransactionDetails.ExternalResourceURL,
		}
	}
	if pay.PointOfInteraction != nil && pay.PointOfInteraction.TransactionData != nil {
		td := pay.PointOfInteraction.TransactionData
		// Pix genera un QR code + payload "copia e cola".
		if td.QRCode != "" || td.QRCodeBase64 != "" {
			res.NextAction = &core.NextAction{
				Type:       core.NextActionQR,
				QRCode:     td.QRCodeBase64,
				QRPayload:  td.QRCode,
			}
		}
	}
	return res
}

// --- mappers ---

func mapPaymentStatus(s string) core.PaymentStatus {
	switch s {
	case "approved":
		return core.StatusCaptured
	case "authorized":
		return core.StatusAuthorized
	case "in_process", "pending":
		return core.StatusPending
	case "rejected":
		return core.StatusFailed
	case "cancelled":
		return core.StatusVoided
	case "refunded":
		return core.StatusRefunded
	case "partially_refunded":
		return core.StatusPartiallyRefunded
	default:
		return core.StatusFailed
	}
}

func mapRefundStatus(s string) core.PaymentStatus {
	switch s {
	case "approved":
		return core.StatusRefunded
	case "in_process", "pending":
		return core.StatusPending
	case "rejected", "cancelled":
		return core.StatusFailed
	default:
		return core.StatusFailed
	}
}

func parseMPDate(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	// MP usa ISO 8601 con timezone: 2025-01-02T15:04:05.000-03:00
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Intentar sin zona.
		t, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Now().UTC()
		}
	}
	return t.UTC()
}

// --- tipos crudos de Mercado Pago (subset) ---

type mpPayment struct {
	ID                 int64                  `json:"id"`
	Status             string                 `json:"status"`
	StatusDetail       string                 `json:"status_detail"`
	TransactionAmount  float64                `json:"transaction_amount"`
	CurrencyID         string                 `json:"currency_id"`
	DateCreated        string                 `json:"date_created"`
	PaymentMethodID    string                 `json:"payment_method_id"`
	TransactionDetails *struct {
		ExternalResourceURL string `json:"external_resource_url"`
	} `json:"transaction_details,omitempty"`
	PointOfInteraction *struct {
		TransactionData *struct {
			QRCode       string `json:"qr_code"`
			QRCodeBase64 string `json:"qr_code_base64"`
		} `json:"transaction_data,omitempty"`
	} `json:"point_of_interaction,omitempty"`
	Raw map[string]any `json:"-"`
}

// UnmarshalJSON captura el body crudo para Raw (debugging/auditoría).
func (p *mpPayment) UnmarshalJSON(b []byte) error {
	type alias mpPayment
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*p = mpPayment(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type mpCardToken struct {
	ID string `json:"id"`
}

type mpRefund struct {
	ID     int64   `json:"id"`
	Status string  `json:"status"`
	Amount float64 `json:"amount"`
}

// parseMPError traduce un error JSON de Mercado Pago a NormalizedError.
//
// Formato MP: {"message": "...", "status": 400, "cause": [{"code": ..., "description": "..."}]}
// Para rechazos de pago, el payment response incluye status="rejected" y
// status_detail="cc_rejected_*". Los errores de API (4xx) vienen en este
// formato.
func parseMPError(body []byte) error {
	var env struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
		Cause   []struct {
			Code        int    `json:"code"`
			Description string `json:"description"`
		} `json:"cause"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider,
			fmt.Sprintf("unparseable mercadopago error: %s", truncate(body)))
	}

	msg := env.Message
	if msg == "" && len(env.Cause) > 0 {
		msg = env.Cause[0].Description
	}

	// Errores de auth del tenant.
	if env.Status == 401 || env.Status == 403 {
		if strings.Contains(strings.ToLower(msg), "invalid token") ||
			strings.Contains(strings.ToLower(msg), "access_token") ||
			strings.Contains(strings.ToLower(msg), "unauthorized") {
			return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, msg)
		}
	}

	// Mapeo por cause.code (códigos numéricos de MP).
	for _, c := range env.Cause {
		switch c.Code {
		case 205, 106:
			return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, c.Description)
		case 208, 209:
			return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, c.Description)
		}
	}

	// Mapeo por status_detail (rechazos de pago).
	if strings.HasPrefix(msg, "cc_rejected_") || containsCause(env.Cause, "cc_rejected_") {
		detail := msg
		if detail == "" {
			detail = env.Cause[0].Description
		}
		return mapMPDecline(detail, msg)
	}

	// Fallback por status HTTP.
	switch {
	case env.Status >= 500:
		return core.NewTransient(core.ErrProviderDown, Provider, msg, nil)
	case env.Status == 429:
		return core.NewTransient(core.ErrRateLimited, Provider, msg, nil)
	case env.Status == 400 || env.Status == 422:
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, msg)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, msg)
	}
}

func containsCause(causes []struct {
	Code        int    `json:"code"`
	Description string `json:"description"`
}, prefix string) bool {
	for _, c := range causes {
		if strings.HasPrefix(c.Description, prefix) {
			return true
		}
	}
	return false
}

// mapMPDecline traduce status_detail de MP a NormalizedError.
func mapMPDecline(detail, msg string) *core.NormalizedError {
	switch detail {
	case "cc_rejected_insufficient_funds", "insufficient_funds":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, detail, detail, msg)
	case "cc_rejected_card_declined", "card_declined":
		return core.NewDecline(core.ErrCardDeclined, Provider, detail, detail, msg)
	case "cc_expired_card", "expired_card":
		return core.NewDecline(core.ErrExpiredCard, Provider, detail, detail, msg)
	case "cc_rejected_bad_filled_security_code", "cc_rejected_security_code_invalid", "invalid_security_code":
		return core.NewDecline(core.ErrInvalidCVC, Provider, detail, detail, msg)
	case "cc_rejected_bad_filled_card_number", "invalid_card_number":
		return core.NewDecline(core.ErrInvalidNumber, Provider, detail, detail, msg)
	case "cc_rejected_fraud", "cc_rejected_fraud_risk", "fraudulent":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, detail, detail, msg)
	case "cc_rejected_call_for_authorize", "cc_rejected_do_not_honor", "do_not_honor":
		return core.NewDecline(core.ErrDoNotHonor, Provider, detail, detail, msg)
	case "cc_rejected_card_disabled", "card_disabled":
		return core.NewDecline(core.ErrCardDeclined, Provider, detail, detail, msg)
	case "cc_rejected_card_error", "processing_error":
		return core.NewDecline(core.ErrProcessingError, Provider, detail, detail, msg)
	case "cc_rejected_max_calls", "rate_limited":
		return core.NewTransient(core.ErrRateLimited, Provider, msg, nil)
	default:
		return core.NewDecline(core.ErrCardDeclined, Provider, detail, detail, msg)
	}
}
