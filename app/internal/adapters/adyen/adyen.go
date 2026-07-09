// Package adyen es el adapter real para la pasarela Adyen.
//
// Implementa core.Gateway invocando a la REST API de Adyen (v71) usando
// únicamente la stdlib de Go (sin SDK externo). Esto mantiene el módulo
// sin dependencias adicionales y el contrato explícito con el proveedor.
//
// Adyen usa autenticación con API key via header X-API-Key. Los montos viajan
// en la unidad mínima (cents) — ya es el formato interno del SDK
// (core.Money.Amount), así que no hay conversión.
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/adyen"
//
// Esto pobla factory.Default y webhook.Default en init().
package adyen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/webhook"
)

// Provider es el ProviderID canónico de Adyen (igual que core.ProviderAdyen).
const Provider core.ProviderID = core.ProviderAdyen

// Caps describe las capabilities estáticas de Adyen para el router.
// Adyen es global (soporta todos los países y monedas que el merchant tenga
// habilitados), soporta auth-only, refunds parciales y vaulting.
var Caps = core.Capabilities{
	Countries:            nil, // global
	Currencies:           nil, // todas las soportadas por la cuenta
	Methods:              []core.PaymentMethod{core.MethodCard},
	SupportsAuthOnly:     true,
	SupportsRefundPartial: true,
	SupportsVaulting:     true,
}

// Base URLs (override vía TenantContext.EndpointURL para proxies/sandbox).
const (
	defaultBaseURL = "https://pal-test.adyen.com"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(&webhook.WebhookHandler{
		Provider:  Provider,
		Verifier:  &adyenVerifier{},
		Normalize: &adyenNormalizer{},
	})
}

// Adapter implementa core.Gateway contra Adyen.
type Adapter struct {
	tctx     *core.TenantContext
	base     string
	merchant string
	hc       *http.Client
}

// New construye un adapter Adyen para el TenantContext dado.
func New(tctx *core.TenantContext) (core.Gateway, error) {
	base := strings.TrimRight(tctx.EndpointURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	// El merchant account se obtiene del TenantContext (campo Secret para Adyen)
	merchant := tctx.Secret
	if merchant == "" {
		return nil, core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider,
			"merchant account is required")
	}
	return &Adapter{
		tctx:     tctx,
		base:     base,
		merchant: merchant,
		hc:       &http.Client{Timeout: defaultTimeout},
	}, nil
}

func (a *Adapter) Provider() core.ProviderID       { return Provider }
func (a *Adapter) Capabilities() core.Capabilities { return Caps }

// --- helpers HTTP ---

// do envía un POST JSON a Adyen con auth X-API-Key.
// Agrega Idempotency-Key cuando op+ref están disponibles para reintentos
// seguros. Devuelve el body decodificado en `out` o un *core.NormalizedError.
func (a *Adapter) do(ctx context.Context, op, ref, path string, body any, out any) error {
	endpoint := a.base + path

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return core.NewError(core.ErrInternal, core.CategoryInternal, Provider, fmt.Sprintf("marshal request: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return core.NewError(core.ErrInternal, core.CategoryInternal, Provider, fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", a.tctx.WebhookSecret) // Usamos WebhookSecret como API key
	if op != "" {
		req.Header.Set("Idempotency-Key", a.tctx.IdempotencyKey(op, ref))
	}

	resp, err := a.hc.Do(req)
	if err != nil {
		// Errores de red → retryable para el cascading.
		return core.NewTransient(core.ErrNetworkError, Provider, fmt.Sprintf("http call: %v", err), err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewTransient(core.ErrNetworkError, Provider, fmt.Sprintf("read body: %v", err), err)
	}

	if resp.StatusCode >= 500 {
		return core.NewTransient(core.ErrProviderDown, Provider,
			fmt.Sprintf("adyen %d: %s", resp.StatusCode, truncate(respBody)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider, "adyen rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseAdyenError(respBody)
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

// Tokenize crea un token en Adyen a partir del token del frontend.
// Adyen normalmente tokeniza en el frontend (Dropin/Components), pero
// algunos flujos requieren crear el token desde el backend.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"adyen tokenize only supports card method")
	}

	req := map[string]any{
		"merchantAccount": a.merchant,
		"paymentMethod": map[string]any{
			"type":  "scheme",
			"token": in.Card.Token,
		},
	}
	if in.Card.Last4 != "" {
		req["paymentMethod"].(map[string]any)["lastFour"] = in.Card.Last4
	}
	if in.Card.Brand != "" {
		req["paymentMethod"].(map[string]any)["brand"] = in.Card.Brand
	}

	var resp adyenTokenizeResp
	if err := a.do(ctx, "tokenize", in.Card.Token, "/pal/servlet/Payment/v64/tokenize", req, &resp); err != nil {
		return nil, err
	}

	return &core.TokenizeResponse{
		ProviderToken: resp.Token,
		Vaulted:       true,
		Method:        core.MethodCard,
		Last4:         in.Card.Last4,
		Brand:         in.Card.Brand,
		Raw:           resp.Raw,
	}, nil
}

// Authorize reserva fondos sin capturarlos (auth-only).
func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	req := a.buildPaymentRequest(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, false)
	var resp adyenPaymentResp
	if err := a.do(ctx, "authorize", in.Reference, "/pal/servlet/Payment/v64/authorise", req, &resp); err != nil {
		return nil, err
	}
	return a.toResult(&resp, in.Reference, in.Amount, in.Method), nil
}

// Charge ejecuta autorización + captura en una sola operación (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	req := a.buildPaymentRequest(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, in.Capture)
	var resp adyenPaymentResp
	if err := a.do(ctx, "charge", in.Reference, "/pal/servlet/Payment/v64/authorise", req, &resp); err != nil {
		return nil, err
	}
	return a.toResult(&resp, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un pago previamente autorizado.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	req := map[string]any{
		"merchantAccount":   a.merchant,
		"originalReference": in.AuthorizationID,
		"modificationAmount": map[string]any{
			"value":    in.Amount.Amount,
			"currency": in.Amount.Currency,
		},
	}
	var resp adyenModificationResp
	if err := a.do(ctx, "capture", in.AuthorizationID, "/pal/servlet/Payment/v64/capture", req, &resp); err != nil {
		return nil, err
	}
	return &core.PaymentResult{
		ID:        resp.PspReference,
		Status:    core.StatusCaptured,
		Method:    core.MethodCard,
		Amount:    in.Amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: in.AuthorizationID,
		CreatedAt: time.Now().UTC(),
		Raw:       resp.Raw,
	}, nil
}

// Void cancela un pago autorizado antes de su captura.
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	req := map[string]any{
		"merchantAccount":   a.merchant,
		"originalReference": in.AuthorizationID,
	}
	if in.Reason != "" {
		req["reason"] = in.Reason
	}
	var resp adyenModificationResp
	if err := a.do(ctx, "void", in.AuthorizationID, "/pal/servlet/Payment/v64/cancel", req, &resp); err != nil {
		return nil, err
	}
	return &core.PaymentResult{
		ID:        resp.PspReference,
		Status:    core.StatusVoided,
		Method:    core.MethodCard,
		Amount:    core.Money{},
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: in.AuthorizationID,
		CreatedAt: time.Now().UTC(),
		Raw:       resp.Raw,
	}, nil
}

// Refund emite un reembolso total o parcial sobre un pago capturado.
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	req := map[string]any{
		"merchantAccount":   a.merchant,
		"originalReference": in.PaymentID,
		"modificationAmount": map[string]any{
			"value":    in.Amount.Amount,
			"currency": in.Amount.Currency,
		},
	}
	var resp adyenModificationResp
	if err := a.do(ctx, "refund", in.PaymentID, "/pal/servlet/Payment/v64/refund", req, &resp); err != nil {
		return nil, err
	}
	return &core.RefundResult{
		ID:        resp.PspReference,
		PaymentID: in.PaymentID,
		Status:    core.StatusRefunded,
		Amount:    in.Amount,
		Provider:  Provider,
		TenantID:  a.tctx.TenantID,
		Reference: in.PaymentID,
		CreatedAt: time.Now().UTC(),
		Raw:       resp.Raw,
	}, nil
}

// --- builders ---

func (a *Adapter) buildPaymentRequest(
	ref string, amount core.Money, method core.PaymentMethod, token string,
	buyer *core.Buyer, desc string, meta map[string]string, capture bool,
) map[string]any {
	req := map[string]any{
		"merchantAccount": a.merchant,
		"amount": map[string]any{
			"value":    amount.Amount,
			"currency": amount.Currency,
		},
		"paymentMethod": map[string]any{
			"type":  "scheme",
			"token": token,
		},
		"reference": ref,
	}
	if capture {
		req["captureDelayHours"] = 0 // Captura inmediata
	} else {
		req["captureDelayHours"] = 120 // Auth-only (48 horas)
	}
	if desc != "" {
		req["description"] = desc
	}
	if buyer != nil {
		req["billingAddress"] = map[string]any{
			"email": buyer.Email,
		}
		if buyer.FirstName != "" || buyer.LastName != "" {
			req["billingAddress"].(map[string]any)["name"] = map[string]any{
				"firstName": buyer.FirstName,
				"lastName":  buyer.LastName,
			}
		}
	}
	if len(meta) > 0 {
		req["metadata"] = meta
	}
	return req
}

func (a *Adapter) toResult(resp *adyenPaymentResp, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        resp.PspReference,
		Status:    mapPaymentStatus(resp.ResultCode),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: time.Now().UTC(),
		Raw:       resp.Raw,
	}
	if resp.Action != nil {
		res.NextAction = mapNextAction(resp.Action)
	}
	return res
}

// --- mappers ---

func mapPaymentStatus(code string) core.PaymentStatus {
	switch code {
	case "Authorised":
		return core.StatusAuthorized
	case "Captured":
		return core.StatusCaptured
	case "Refunded":
		return core.StatusRefunded
	case "Cancelled":
		return core.StatusVoided
	case "Pending":
		return core.StatusPending
	case "Received":
		return core.StatusPending
	default:
		return core.StatusFailed
	}
}

func mapNextAction(action *adyenAction) *core.NextAction {
	if action == nil {
		return nil
	}
	out := &core.NextAction{}
	switch action.Type {
	case "redirect":
		out.Type = core.NextActionRedirect
		out.RedirectURL = action.URL
	case "threeDS2":
		out.Type = core.NextAction3DS
		out.Token3DS = action.Token
	default:
		out.Type = core.NextActionWait
	}
	return out
}

// --- tipos crudos de Adyen (subset) ---

type adyenTokenizeResp struct {
	Token string         `json:"token"`
	Raw   map[string]any `json:"-"`
}

func (r *adyenTokenizeResp) UnmarshalJSON(b []byte) error {
	type alias adyenTokenizeResp
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*r = adyenTokenizeResp(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		r.Raw = raw
	}
	return nil
}

type adyenPaymentResp struct {
	PspReference string       `json:"pspReference"`
	ResultCode   string       `json:"resultCode"`
	Action       *adyenAction `json:"action,omitempty"`
	Raw          map[string]any `json:"-"`
}

func (r *adyenPaymentResp) UnmarshalJSON(b []byte) error {
	type alias adyenPaymentResp
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*r = adyenPaymentResp(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		r.Raw = raw
	}
	return nil
}

type adyenAction struct {
	Type  string `json:"type"`
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

type adyenModificationResp struct {
	PspReference string         `json:"pspReference"`
	Response     string         `json:"response"`
	Raw          map[string]any `json:"-"`
}

func (r *adyenModificationResp) UnmarshalJSON(b []byte) error {
	type alias adyenModificationResp
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*r = adyenModificationResp(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		r.Raw = raw
	}
	return nil
}

// parseAdyenError traduce un error JSON de Adyen a NormalizedError.
func parseAdyenError(body []byte) error {
	var errResp struct {
		ErrorCode   string `json:"errorCode"`
		Message     string `json:"message"`
		ErrorType   string `json:"errorType"`
		Status      int    `json:"status"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider,
			fmt.Sprintf("unparseable adyen error: %s", truncate(body)))
	}

	// Errores de auth del tenant.
	if errResp.Status == 401 || errResp.ErrorCode == "901" {
		return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, errResp.Message).
			Wrap(fmt.Errorf("adyen: %s", errResp.ErrorCode))
	}

	// Mapeo de error codes → ErrorCode canónico.
	switch errResp.ErrorCode {
	case "101", "902":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, errResp.Message)
	case "103":
		return core.NewDecline(core.ErrCardDeclined, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "104":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "105":
		return core.NewDecline(core.ErrExpiredCard, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "106":
		return core.NewDecline(core.ErrInvalidCVC, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "107":
		return core.NewDecline(core.ErrInvalidNumber, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "140":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "141":
		return core.NewDecline(core.ErrDoNotHonor, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "142":
		return core.NewDecline(core.ErrLostCard, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "143":
		return core.NewDecline(core.ErrStolenCard, Provider, errResp.ErrorCode, errResp.ErrorCode, errResp.Message)
	case "170":
		return core.NewTransient(core.ErrRateLimited, Provider, errResp.Message, nil)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, errResp.Message).
			Wrap(fmt.Errorf("adyen code=%s type=%s", errResp.ErrorCode, errResp.ErrorType))
	}
}
