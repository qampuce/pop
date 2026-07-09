// Package dlocal es el adapter real para la pasarela dLocal.
//
// Implementa core.Gateway invocando a la REST API de dLocal usando
// únicamente la stdlib de Go (sin SDK externo). Mantiene el módulo sin
// dependencias adicionales y el contrato explícito con el proveedor.
//
// dLocal usa autenticación con API key + firma HMAC-SHA256 en header X-Signature.
// Los montos viajan en la unidad mínima de la moneda (cents) — coincide con
// el formato interno del SDK (core.Money.Amount), así que no hay conversión.
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/dlocal"
//
// Esto pobla factory.Default y webhook.Default en init().
package dlocal

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/webhook"
)

// Provider es el ProviderID canónico de dLocal.
const Provider core.ProviderID = core.ProviderDLocal

// Caps describe las capabilities estáticas de dLocal para el router.
// dLocal está disponible en toda Latam, soporta auth-only, refunds
// parciales y vaulting.
var Caps = core.Capabilities{
	Countries: []string{
		"AR", "BR", "CL", "CO", "MX", "PE", "UY", "EC", "VE",
		"CR", "DO", "PA", "PY", "BO", "NI", "SV", "GT", "HN",
	},
	Currencies: []string{
		"ARS", "BRL", "CLP", "COP", "MXN", "PEN", "UYU", "USD",
		"CRC", "DOP", "PAB", "PYG", "BOB", "VES", "NIO", "GTQ",
		"HNL",
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
	defaultBaseURL = "https://api.dlocal.com"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(Provider, &dlocalVerifier{}, &dlocalNormalizer{})
}

// Adapter implementa core.Gateway contra dLocal.
type Adapter struct {
	tctx *core.TenantContext
	base string
	hc   *http.Client
}

// New construye un adapter dLocal para el TenantContext dado.
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

// doJSON envía una request JSON a dLocal con auth (X-Date + X-Signature).
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

	// dLocal usa X-Date (RFC 2822) + X-Signature (HMAC-SHA256 de X-Date + body)
	date := time.Now().UTC().Format(time.RFC1123)
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Login", a.tctx.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Calcular firma HMAC-SHA256
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	signature := a.sign(date, bodyBytes)
	req.Header.Set("X-Signature", signature)

	if op != "" {
		req.Header.Set("X-Idempotency-Key", a.tctx.IdempotencyKey(op, ref))
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
			fmt.Sprintf("dlocal %d: %s", resp.StatusCode, truncate(respBody)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider,
			"dlocal rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseDLocalError(respBody)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return core.NewError(core.ErrInternal, core.CategoryInternal, Provider,
				fmt.Sprintf("decode response: %v", err))
		}
	}
	return nil
}

// sign genera la firma HMAC-SHA256 para dLocal.
// Firma = HMAC-SHA256(X-Date + body, secret_key)
func (a *Adapter) sign(date string, body []byte) string {
	h := hmac.New(sha256.New, []byte(a.tctx.WebhookSecret))
	h.Write([]byte(date))
	if len(body) > 0 {
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func truncate(b []byte) string {
	const max = 512
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// --- Operaciones Gateway ---

// Tokenize crea un token de tarjeta en dLocal a partir del token del frontend.
// dLocal permite tokenizar en el frontend (SDK JS) y reusar el token; este método
// re-tokeniza desde el backend cuando el tenant necesita vaulting explícito.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"dlocal tokenize only supports card method")
	}

	payload := map[string]any{
		"card": map[string]string{
			"token": in.Card.Token,
		},
	}
	if in.Buyer != nil && in.Buyer.Email != "" {
		payload["payer"] = map[string]string{
			"email": in.Buyer.Email,
		}
	}

	var tok dlocalToken
	if err := a.doJSON(ctx, http.MethodPost, "tokenize", in.Card.Token, "/secure_payments", payload, &tok); err != nil {
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
	var pay dlocalPayment
	if err := a.doJSON(ctx, http.MethodPost, "authorize", in.Reference, "/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Charge crea un payment con capture=true (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	payload := a.paymentPayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, in.Capture)
	var pay dlocalPayment
	if err := a.doJSON(ctx, http.MethodPost, "charge", in.Reference, "/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un payment previamente autorizado.
// dLocal usa POST /payments/:id/capture.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	payload := map[string]any{
		"amount": float64(in.Amount.Amount) / 100.0,
		"currency": strings.ToUpper(in.Amount.Currency),
	}
	var pay dlocalPayment
	path := "/payments/" + in.AuthorizationID + "/capture"
	if err := a.doJSON(ctx, http.MethodPost, "capture", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, in.Amount, core.MethodCard), nil
}

// Void cancela un payment que está en state authorized/pendiente.
// dLocal usa POST /payments/:id/cancel.
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	payload := map[string]any{}
	if in.Reason != "" {
		payload["reason"] = in.Reason
	}
	var pay dlocalPayment
	path := "/payments/" + in.AuthorizationID + "/cancel"
	if err := a.doJSON(ctx, http.MethodPost, "void", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, core.Money{}, core.MethodCard), nil
}

// Refund emite un reembolso total o parcial sobre un payment.
// dLocal usa POST /payments/:id/refunds.
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	payload := map[string]any{
		"amount": float64(in.Amount.Amount) / 100.0,
		"currency": strings.ToUpper(in.Amount.Currency),
		"reason": mapRefundReason(in.Reason),
	}
	var r dlocalRefund
	path := "/payments/" + in.PaymentID + "/refunds"
	if err := a.doJSON(ctx, http.MethodPost, "refund", in.PaymentID, path, payload, &r); err != nil {
		return nil, err
	}
	return &core.RefundResult{
		ID:        r.ID,
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
		"amount":         float64(amount.Amount) / 100.0,
		"currency":       strings.ToUpper(amount.Currency),
		"capture":        capture,
		"order_id":       ref,
		"metadata":       mergeMeta(meta, map[string]string{"reference": ref}),
	}
	if desc != "" {
		out["description"] = desc
	}
	// Token de tarjeta del frontend (card) o datos APM.
	if token != "" {
		out["card"] = map[string]string{
			"token": token,
		}
	}
	// APMs: Pix/SPEI/PSE/PagoEfectivo se seleccionan vía payment_method_id.
	switch method {
	case core.MethodPix:
		out["payment_method_id"] = "PX"
		delete(out, "card")
	case core.MethodSPEI:
		out["payment_method_id"] = "SP"
		delete(out, "card")
	case core.MethodPSE:
		out["payment_method_id"] = "PS"
		delete(out, "card")
	case core.MethodPagoEfectivo:
		out["payment_method_id"] = "PE"
		delete(out, "card")
	}
	if buyer != nil {
		p := map[string]any{}
		if buyer.Email != "" {
			p["email"] = buyer.Email
		}
		if buyer.FirstName != "" || buyer.LastName != "" {
			p["name"] = strings.TrimSpace(buyer.FirstName + " " + buyer.LastName)
		}
		if buyer.DocType != "" {
			p["document"] = map[string]string{
				"type":   buyer.DocType,
				"number": buyer.DocNumber,
			}
		}
		if buyer.IP != "" {
			p["ip"] = buyer.IP
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

func (a *Adapter) toResult(pay *dlocalPayment, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        pay.ID,
		Status:    mapPaymentStatus(pay.Status),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: parseDLocalDate(pay.CreatedAt),
		Raw:       pay.Raw,
	}
	if pay.RedirectURL != "" {
		// APMs generan un redirect URL.
		res.NextAction = &core.NextAction{
			Type:        core.NextActionRedirect,
			RedirectURL: pay.RedirectURL,
		}
	}
	if pay.QRCode != "" {
		// Pix genera un QR code.
		res.NextAction = &core.NextAction{
			Type:   core.NextActionQR,
			QRCode: pay.QRCode,
		}
	}
	return res
}

// --- mappers ---

func mapPaymentStatus(s string) core.PaymentStatus {
	switch s {
	case "PAID":
		return core.StatusCaptured
	case "AUTHORIZED":
		return core.StatusAuthorized
	case "PENDING":
		return core.StatusPending
	case "REJECTED":
		return core.StatusFailed
	case "CANCELLED":
		return core.StatusVoided
	case "REFUNDED":
		return core.StatusRefunded
	case "PARTIALLY_REFUNDED":
		return core.StatusPartiallyRefunded
	default:
		return core.StatusFailed
	}
}

func mapRefundStatus(s string) core.PaymentStatus {
	switch s {
	case "SUCCESS":
		return core.StatusRefunded
	case "PENDING":
		return core.StatusPending
	case "FAILED":
		return core.StatusFailed
	default:
		return core.StatusFailed
	}
}

func mapRefundReason(r core.RefundReason) string {
	switch r {
	case core.RefundDuplicate:
		return "DUPLICATE"
	case core.RefundFraudulent:
		return "FRAUD"
	case core.RefundRequestedByCustomer:
		return "CUSTOMER_REQUEST"
	case core.RefundProductNotReceived:
		return "PRODUCT_NOT_RECEIVED"
	default:
		return "CUSTOMER_REQUEST"
	}
}

func parseDLocalDate(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	// dLocal usa ISO 8601: 2025-01-02T15:04:05.000Z
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

// --- tipos crudos de dLocal (subset) ---

type dlocalPayment struct {
	ID          string        `json:"id"`
	Status      string        `json:"status"`
	Amount      float64       `json:"amount"`
	Currency    string        `json:"currency"`
	CreatedAt   string        `json:"created_at"`
	RedirectURL string        `json:"redirect_url,omitempty"`
	QRCode      string        `json:"qr_code,omitempty"`
	Raw         map[string]any `json:"-"`
}

// UnmarshalJSON captura el body crudo para Raw (debugging/auditoría).
func (p *dlocalPayment) UnmarshalJSON(b []byte) error {
	type alias dlocalPayment
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*p = dlocalPayment(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type dlocalToken struct {
	ID string `json:"id"`
}

type dlocalRefund struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Amount float64 `json:"amount"`
}

// parseDLocalError traduce un error JSON de dLocal a NormalizedError.
//
// Formato dLocal: {"code": "...", "message": "...", "errors": [...]}
func parseDLocalError(body []byte) error {
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider,
			fmt.Sprintf("unparseable dlocal error: %s", truncate(body)))
	}

	msg := env.Message
	if msg == "" && len(env.Errors) > 0 {
		msg = env.Errors[0].Reason
	}

	// Errores de auth del tenant.
	if env.Code == "4001" || env.Code == "4002" {
		return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, msg)
	}

	// Mapeo por código de error.
	switch env.Code {
	case "3001":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, msg)
	case "5001":
		return core.NewDecline(core.ErrCardDeclined, Provider, env.Code, env.Code, msg)
	case "5002":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, env.Code, env.Code, msg)
	case "5003":
		return core.NewDecline(core.ErrExpiredCard, Provider, env.Code, env.Code, msg)
	case "5004":
		return core.NewDecline(core.ErrInvalidCVC, Provider, env.Code, env.Code, msg)
	case "5005":
		return core.NewDecline(core.ErrInvalidNumber, Provider, env.Code, env.Code, msg)
	case "5006":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, env.Code, env.Code, msg)
	case "5007":
		return core.NewDecline(core.ErrDoNotHonor, Provider, env.Code, env.Code, msg)
	case "5008":
		return core.NewDecline(core.ErrProcessingError, Provider, env.Code, env.Code, msg)
	case "5009":
		return core.NewTransient(core.ErrRateLimited, Provider, msg, nil)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, msg).
			Wrap(fmt.Errorf("dlocal code=%s", env.Code))
	}
}
