// Package niubiz es el adapter real para la pasarela Niubiz (Perú).
//
// Implementa core.Gateway invocando a la REST API de Niubiz usando
// únicamente la stdlib de Go (sin SDK externo). Mantiene el módulo sin
// dependencias adicionales y el contrato explícito con el proveedor.
//
// Niubiz usa autenticación Basic con la API key del integrador.
// Los montos viajan en la unidad mínima de la moneda (céntimos para PEN) —
// coincide con el formato interno del SDK (core.Money.Amount).
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/niubiz"
//
// Esto pobla factory.Default y webhook.Default en init().
package niubiz

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

// Provider es el ProviderID canónico de Niubiz.
const Provider core.ProviderID = core.ProviderNiubiz

// Caps describe las capabilities estáticas de Niubiz para el router.
// Niubiz está disponible en Perú, soporta auth-only, refunds parciales
// y vaulting. Soporta APMs locales como Yape y Plin.
var Caps = core.Capabilities{
	Countries: []string{
		"PE",
	},
	Currencies: []string{
		"PEN", "USD",
	},
	Methods: []core.PaymentMethod{
		core.MethodCard,
		core.MethodYape,
		core.MethodPlin,
	},
	SupportsAuthOnly:      true,
	SupportsRefundPartial: true,
	SupportsVaulting:      true,
}

// Base URLs (override vía TenantContext.EndpointURL para proxies/sandbox).
const (
	defaultBaseURL = "https://api.niubiz.com.pe"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(&webhook.WebhookHandler{
		Provider:  Provider,
		Verifier:  &niubizVerifier{},
		Normalize: &niubizNormalizer{},
	})
}

// Adapter implementa core.Gateway contra Niubiz.
type Adapter struct {
	tctx *core.TenantContext
	base string
	hc   *http.Client
}

// New construye un adapter Niubiz para el TenantContext dado.
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

// doJSON envía una request JSON a Niubiz con auth Basic (API key).
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
	req.SetBasicAuth(a.tctx.Secret, "")
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
			fmt.Sprintf("niubiz %d: %s", resp.StatusCode, truncate(respBody)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider,
			"niubiz rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseNiubizError(respBody)
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

// Tokenize crea un token de tarjeta en Niubiz a partir del token del frontend.
// Niubiz permite tokenizar en el frontend (SDK JS) y reusar el token; este método
// re-tokeniza desde el backend cuando el tenant necesita vaulting explícito.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"niubiz tokenize only supports card method")
	}

	payload := map[string]any{
		"card": map[string]string{
			"token": in.Card.Token,
		},
	}
	if in.Card.Last4 != "" {
		payload["card"].(map[string]string)["last4"] = in.Card.Last4
	}
	if in.Card.Brand != "" {
		payload["card"].(map[string]string)["brand"] = in.Card.Brand
	}
	if in.Buyer != nil && in.Buyer.Email != "" {
		payload["email"] = in.Buyer.Email
	}

	var tok niubizToken
	if err := a.doJSON(ctx, http.MethodPost, "tokenize", in.Card.Token, "/v1/tokens", payload, &tok); err != nil {
		return nil, err
	}
	return &core.TokenizeResponse{
		ProviderToken: tok.Token,
		Vaulted:       true,
		Method:        core.MethodCard,
		Last4:         in.Card.Last4,
		Brand:         in.Card.Brand,
	}, nil
}

// Authorize crea un payment con capture=false (auth-only).
func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	payload := a.paymentPayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, false)
	var pay niubizPayment
	if err := a.doJSON(ctx, http.MethodPost, "authorize", in.Reference, "/v1/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Charge crea un payment con capture=true (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	payload := a.paymentPayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, in.Capture)
	var pay niubizPayment
	if err := a.doJSON(ctx, http.MethodPost, "charge", in.Reference, "/v1/payments", payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un payment previamente autorizado.
// Niubiz usa POST /v1/payments/:id/capture.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	payload := map[string]any{
		"amount":   float64(in.Amount.Amount) / 100.0,
		"currency": strings.ToUpper(in.Amount.Currency),
	}
	var pay niubizPayment
	path := "/v1/payments/" + in.AuthorizationID + "/capture"
	if err := a.doJSON(ctx, http.MethodPost, "capture", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, in.Amount, core.MethodCard), nil
}

// Void cancela un payment que está en state authorized/pendiente.
// Niubiz usa POST /v1/payments/:id/void.
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	payload := map[string]any{}
	if in.Reason != "" {
		payload["reason"] = in.Reason
	}
	var pay niubizPayment
	path := "/v1/payments/" + in.AuthorizationID + "/void"
	if err := a.doJSON(ctx, http.MethodPost, "void", in.AuthorizationID, path, payload, &pay); err != nil {
		return nil, err
	}
	return a.toResult(&pay, in.AuthorizationID, core.Money{}, core.MethodCard), nil
}

// Refund emite un reembolso total o parcial sobre un payment.
// Niubiz usa POST /v1/payments/:id/refunds.
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	payload := map[string]any{
		"amount":   float64(in.Amount.Amount) / 100.0,
		"currency": strings.ToUpper(in.Amount.Currency),
		"reason":   mapRefundReason(in.Reason),
	}
	var r niubizRefund
	path := "/v1/payments/" + in.PaymentID + "/refunds"
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
	// APMs: Yape/Plin se seleccionan vía payment_method_id.
	switch method {
	case core.MethodYape:
		out["payment_method_id"] = "YAPE"
		delete(out, "card")
	case core.MethodPlin:
		out["payment_method_id"] = "PLIN"
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
		if buyer.Phone != "" {
			p["phone"] = buyer.Phone
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

func (a *Adapter) toResult(pay *niubizPayment, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        pay.ID,
		Status:    mapPaymentStatus(pay.Status),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: parseNiubizDate(pay.CreatedAt),
		Raw:       pay.Raw,
	}
	if pay.RedirectURL != "" {
		// APMs (Yape/Plin) generan un redirect URL o deep link.
		res.NextAction = &core.NextAction{
			Type:        core.NextActionRedirect,
			RedirectURL: pay.RedirectURL,
		}
	}
	if pay.DeepLink != "" {
		// Yape/Plin pueden usar deep link para app switch.
		res.NextAction = &core.NextAction{
			Type:     core.NextActionAppSwitch,
			DeepLink: pay.DeepLink,
		}
	}
	if pay.QRCode != "" {
		// Yape/Plin pueden generar QR code.
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
	case "SUCCESS":
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
	case "REJECTED":
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

func parseNiubizDate(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	// Niubiz usa ISO 8601: 2025-01-02T15:04:05.000Z
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

// --- tipos crudos de Niubiz (subset) ---

type niubizPayment struct {
	ID          string        `json:"id"`
	Status      string        `json:"status"`
	Amount      float64       `json:"amount"`
	Currency    string        `json:"currency"`
	CreatedAt   string        `json:"created_at"`
	RedirectURL string        `json:"redirect_url,omitempty"`
	DeepLink    string        `json:"deep_link,omitempty"`
	QRCode      string        `json:"qr_code,omitempty"`
	Raw         map[string]any `json:"-"`
}

// UnmarshalJSON captura el body crudo para Raw (debugging/auditoría).
func (p *niubizPayment) UnmarshalJSON(b []byte) error {
	type alias niubizPayment
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*p = niubizPayment(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type niubizToken struct {
	Token string `json:"token"`
}

type niubizRefund struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Amount float64 `json:"amount"`
}

// parseNiubizError traduce un error JSON de Niubiz a NormalizedError.
//
// Formato Niubiz: {"code": "...", "message": "...", "errors": [...]}
func parseNiubizError(body []byte) error {
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
			fmt.Sprintf("unparseable niubiz error: %s", truncate(body)))
	}

	msg := env.Message
	if msg == "" && len(env.Errors) > 0 {
		msg = env.Errors[0].Reason
	}

	// Errores de auth del tenant.
	if env.Code == "401" || env.Code == "403" {
		return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, msg)
	}

	// Mapeo por código de error.
	switch env.Code {
	case "400":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, msg)
	case "1001":
		return core.NewDecline(core.ErrCardDeclined, Provider, env.Code, env.Code, msg)
	case "1002":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, env.Code, env.Code, msg)
	case "1003":
		return core.NewDecline(core.ErrExpiredCard, Provider, env.Code, env.Code, msg)
	case "1004":
		return core.NewDecline(core.ErrInvalidCVC, Provider, env.Code, env.Code, msg)
	case "1005":
		return core.NewDecline(core.ErrInvalidNumber, Provider, env.Code, env.Code, msg)
	case "1006":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, env.Code, env.Code, msg)
	case "1007":
		return core.NewDecline(core.ErrDoNotHonor, Provider, env.Code, env.Code, msg)
	case "1008":
		return core.NewDecline(core.ErrProcessingError, Provider, env.Code, env.Code, msg)
	case "1009":
		return core.NewTransient(core.ErrRateLimited, Provider, msg, nil)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, msg).
			Wrap(fmt.Errorf("niubiz code=%s", env.Code))
	}
}
