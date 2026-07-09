// Package stripe es el adapter real para la pasarela Stripe.
//
// Implementa core.Gateway invocando a la REST API de Stripe (v1) usando
// únicamente la stdlib de Go (sin SDK externo). Esto mantiene el módulo
// sin dependencias adicionales y el contrato explícito con el proveedor.
//
// Stripe usa autenticación Basic con la secret key como username y password
// vacía. Los montos viajan en la unidad mínima (cents) — ya es el formato
// interno del SDK (core.Money.Amount), así que no hay conversión.
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/stripe"
//
// Esto pobla factory.Default y webhook.Default en init().
package stripe

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

// Provider es el ProviderID canónico de Stripe (igual que core.ProviderStripe).
const Provider core.ProviderID = core.ProviderStripe

// Caps describe las capabilities estáticas de Stripe para el router.
// Stripe es global (soporta todos los países y monedas que el merchant tenga
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
	defaultBaseURL = "https://api.stripe.com/v1"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(Provider, &stripeVerifier{}, &stripeNormalizer{})
}

// Adapter implementa core.Gateway contra Stripe.
type Adapter struct {
	tctx *core.TenantContext
	base string
	hc   *http.Client
}

// New construye un adapter Stripe para el TenantContext dado.
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

// do envía un POST form-encoded a Stripe con auth Basic (secret key).
// Agrega Idempotency-Key cuando op+ref están disponibles para reintentos
// seguros. Devuelve el body decodificado en `out` o un *core.NormalizedError.
func (a *Adapter) do(ctx context.Context, op, ref, path string, form url.Values, out any) error {
	endpoint := a.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return core.NewError(core.ErrInternal, core.CategoryInternal, Provider, fmt.Sprintf("build request: %v", err))
	}
	req.SetBasicAuth(a.tctx.Secret, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", "2024-04-10")
	if op != "" {
		req.Header.Set("Idempotency-Key", a.tctx.IdempotencyKey(op, ref))
	}

	resp, err := a.hc.Do(req)
	if err != nil {
		// Errores de red → retryable para el cascading.
		return core.NewTransient(core.ErrNetworkError, Provider, fmt.Sprintf("http call: %v", err), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.NewTransient(core.ErrNetworkError, Provider, fmt.Sprintf("read body: %v", err), err)
	}

	if resp.StatusCode >= 500 {
		return core.NewTransient(core.ErrProviderDown, Provider,
			fmt.Sprintf("stripe %d: %s", resp.StatusCode, truncate(body)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider, "stripe rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseStripeError(body)
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
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

// Tokenize crea un PaymentMethod en Stripe a partir del token del frontend.
// Stripe normalmente tokeniza en el frontend (PaymentIntent client secret),
// pero algunos flujos requieren crear el PM desde el backend con datos no
// sensibles (last4 ya tokenizado). Para card, pasamos el token del frontend
// como source.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"stripe tokenize only supports card method")
	}
	form := url.Values{}
	form.Set("type", "card")
	form.Set("card[token]", in.Card.Token)
	if in.Card.Last4 != "" {
		form.Set("card[last4]", in.Card.Last4)
	}
	if in.Card.Brand != "" {
		form.Set("card[brand]", in.Card.Brand)
	}
	if in.Buyer != nil && in.Buyer.Email != "" {
		form.Set("billing_details[email]", in.Buyer.Email)
	}

	var pm stripePaymentMethod
	if err := a.do(ctx, "tokenize", in.Card.Token, "/payment_methods", form, &pm); err != nil {
		return nil, err
	}
	return &core.TokenizeResponse{
		ProviderToken: pm.ID,
		Vaulted:       true,
		Method:        core.MethodCard,
		Last4:         pm.Card.Last4,
		Brand:         pm.Card.Brand,
		Raw:           pm.Raw,
	}, nil
}

// Authorize crea un PaymentIntent con capture=false (auth-only).
func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	form := a.intentForm(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, false)
	var pi stripePaymentIntent
	if err := a.do(ctx, "authorize", in.Reference, "/payment_intents", form, &pi); err != nil {
		return nil, err
	}
	return a.toResult(&pi, in.Reference, in.Amount, in.Method), nil
}

// Charge crea un PaymentIntent con capture=true (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	capture := in.Capture
	form := a.intentForm(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, capture)
	var pi stripePaymentIntent
	if err := a.do(ctx, "charge", in.Reference, "/payment_intents", form, &pi); err != nil {
		return nil, err
	}
	return a.toResult(&pi, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un PaymentIntent previamente autorizado.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	form := url.Values{}
	if in.Amount.Amount > 0 {
		form.Set("amount_to_capture", strconv.FormatInt(in.Amount.Amount, 10))
	}
	var pi stripePaymentIntent
	path := "/payment_intents/" + in.AuthorizationID + "/capture"
	if err := a.do(ctx, "capture", in.AuthorizationID, path, form, &pi); err != nil {
		return nil, err
	}
	return a.toResult(&pi, in.AuthorizationID, in.Amount, core.MethodCard), nil
}

// Void cancela un PaymentIntent que está en state requires_capture.
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	form := url.Values{}
	if in.Reason != "" {
		form.Set("cancellation_reason", in.Reason)
	}
	var pi stripePaymentIntent
	path := "/payment_intents/" + in.AuthorizationID + "/cancel"
	if err := a.do(ctx, "void", in.AuthorizationID, path, form, &pi); err != nil {
		return nil, err
	}
	return a.toResult(&pi, in.AuthorizationID, core.Money{}, core.MethodCard), nil
}

// Refund emite un reembolso total o parcial sobre un PaymentIntent.
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	form := url.Values{}
	form.Set("payment_intent", in.PaymentID)
	if in.Amount.Amount > 0 {
		form.Set("amount", strconv.FormatInt(in.Amount.Amount, 10))
	}
	form.Set("reason", mapRefundReason(in.Reason))
	var r stripeRefund
	if err := a.do(ctx, "refund", in.PaymentID, "/refunds", form, &r); err != nil {
		return nil, err
	}
	return &core.RefundResult{
		ID:        r.ID,
		PaymentID: in.PaymentID,
		Status:    mapRefundStatus(r.Status),
		Amount:    core.Money{Amount: r.Amount, Currency: r.Currency},
		Provider:  Provider,
		TenantID:  a.tctx.TenantID,
		Reference: in.PaymentID,
		CreatedAt: time.Unix(r.Created, 0).UTC(),
		Raw:       r.Raw,
	}, nil
}

// --- builders ---

func (a *Adapter) intentForm(
	ref string, amount core.Money, method core.PaymentMethod, token string,
	buyer *core.Buyer, desc string, meta map[string]string, capture bool,
) url.Values {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(amount.Amount, 10))
	form.Set("currency", strings.ToLower(amount.Currency))
	form.Set("payment_method", token)
	form.Set("confirm", "true")
	if capture {
		form.Set("capture_method", "automatic")
	} else {
		form.Set("capture_method", "manual")
	}
	form.Set("statement_descriptor_suffix", ref)
	if desc != "" {
		form.Set("description", desc)
	}
	if buyer != nil {
		if buyer.Email != "" {
			form.Set("receipt_email", buyer.Email)
		}
		if buyer.FirstName != "" || buyer.LastName != "" {
			form.Set("shipping[name]", strings.TrimSpace(buyer.FirstName+" "+buyer.LastName))
		}
	}
	for k, v := range meta {
		form.Set("metadata["+k+"]", v)
	}
	return form
}

func (a *Adapter) toResult(pi *stripePaymentIntent, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        pi.ID,
		Status:    mapPIStatus(pi.Status),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: time.Unix(pi.Created, 0).UTC(),
		Raw:       pi.Raw,
	}
	if pi.NextAction != nil {
		res.NextAction = mapNextAction(pi.NextAction)
	}
	return res
}

// --- mappers ---

func mapPIStatus(s string) core.PaymentStatus {
	switch s {
	case "requires_payment_method", "requires_confirmation":
		return core.StatusFailed
	case "requires_action":
		return core.StatusPending
	case "requires_capture":
		return core.StatusAuthorized
	case "succeeded":
		return core.StatusCaptured
	case "canceled":
		return core.StatusVoided
	case "processing":
		return core.StatusPending
	default:
		return core.StatusFailed
	}
}

func mapRefundStatus(s string) core.PaymentStatus {
	switch s {
	case "succeeded":
		return core.StatusRefunded
	case "pending":
		return core.StatusPending
	case "failed":
		return core.StatusFailed
	case "canceled":
		return core.StatusFailed
	default:
		return core.StatusFailed
	}
}

func mapRefundReason(r core.RefundReason) string {
	switch r {
	case core.RefundDuplicate:
		return "duplicate"
	case core.RefundFraudulent:
		return "fraudulent"
	case core.RefundRequestedByCustomer:
		return "requested_by_customer"
	case core.RefundProductNotReceived:
		return "requested_by_customer" // Stripe no tiene product_not_received
	default:
		return "requested_by_customer"
	}
}

func mapNextAction(na *stripeNextAction) *core.NextAction {
	out := &core.NextAction{}
	switch na.Type {
	case "redirect_to_url":
		out.Type = core.NextActionRedirect
		if na.RedirectToURL != nil {
			out.RedirectURL = na.RedirectToURL.URL
		}
	case "use_stripe_sdk":
		out.Type = core.NextAction3DS
		out.Token3DS = na.SDKData
	case "verify_with_microdeposits":
		out.Type = core.NextActionWait
	default:
		out.Type = core.NextActionWait
	}
	return out
}

// --- tipos crudos de Stripe (subset) ---

type stripePaymentIntent struct {
	ID          string                 `json:"id"`
	Status      string                 `json:"status"`
	Amount      int64                  `json:"amount"`
	Currency    string                 `json:"currency"`
	Created     int64                  `json:"created"`
	NextAction  *stripeNextAction      `json:"next_action,omitempty"`
	Raw         map[string]any         `json:"-"` // poblar aparte
	rawBody     json.RawMessage        // no exportado
}

// UnmarshalJSON captura el body crudo para Raw (debugging/auditoría).
func (p *stripePaymentIntent) UnmarshalJSON(b []byte) error {
	type alias stripePaymentIntent
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*p = stripePaymentIntent(tmp)
	p.rawBody = append([]byte(nil), b...)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type stripeNextAction struct {
	Type          string `json:"type"`
	RedirectToURL *struct {
		URL string `json:"url"`
	} `json:"redirect_to_url,omitempty"`
	SDKData string `json:"stripe_js,omitempty"`
}

type stripePaymentMethod struct {
	ID   string `json:"id"`
	Card struct {
		Last4 string `json:"last4"`
		Brand string `json:"brand"`
	} `json:"card"`
	Raw map[string]any `json:"-"`
}

func (p *stripePaymentMethod) UnmarshalJSON(b []byte) error {
	type alias stripePaymentMethod
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*p = stripePaymentMethod(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type stripeRefund struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
	Created  int64  `json:"created"`
	Raw      map[string]any `json:"-"`
}

func (r *stripeRefund) UnmarshalJSON(b []byte) error {
	type alias stripeRefund
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*r = stripeRefund(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		r.Raw = raw
	}
	return nil
}

// parseStripeError traduce un error JSON de Stripe a NormalizedError.
//
// Formato Stripe: {"error": {"type": "...", "code": "...", "decline_code":
// "...", "message": "...", "doc_url": "..."}}
func parseStripeError(body []byte) error {
	var env struct {
		Error struct {
			Type        string `json:"type"`
			Code        string `json:"code"`
			DeclineCode string `json:"decline_code"`
			Message     string `json:"message"`
			DocURL      string `json:"doc_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider,
			fmt.Sprintf("unparseable stripe error: %s", truncate(body)))
	}
	e := env.Error

	// Errores de auth del tenant.
	switch e.Type {
	case "authentication_error", "invalid_request_error":
		if e.Code == "invalid_api_key" || strings.Contains(strings.ToLower(e.Message), "api key") {
			return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, e.Message).
				Wrap(fmt.Errorf("stripe: %s", e.Code))
		}
	}

	// Mapeo de decline codes → ErrorCode canónico.
	if e.DeclineCode != "" {
		return mapDecline(e.DeclineCode, e.Code, e.Message)
	}

	// Mapeo por code.
	switch e.Code {
	case "card_declined":
		return core.NewDecline(core.ErrCardDeclined, Provider, "card_declined", e.Code, e.Message)
	case "insufficient_funds":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, "insufficient_funds", e.Code, e.Message)
	case "expired_card":
		return core.NewDecline(core.ErrExpiredCard, Provider, "expired_card", e.Code, e.Message)
	case "incorrect_cvc":
		return core.NewDecline(core.ErrInvalidCVC, Provider, "incorrect_cvc", e.Code, e.Message)
	case "incorrect_number":
		return core.NewDecline(core.ErrInvalidNumber, Provider, "incorrect_number", e.Code, e.Message)
	case "processing_error":
		return core.NewDecline(core.ErrProcessingError, Provider, "processing_error", e.Code, e.Message)
	case "rate_limit":
		return core.NewTransient(core.ErrRateLimited, Provider, e.Message, nil)
	case "resource_missing":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, e.Message)
	}

	// Fallback por type.
	switch e.Type {
	case "api_error":
		return core.NewTransient(core.ErrProviderDown, Provider, e.Message, nil)
	case "card_error":
		return core.NewDecline(core.ErrCardDeclined, Provider, e.DeclineCode, e.Code, e.Message)
	case "rate_limit_error":
		return core.NewTransient(core.ErrRateLimited, Provider, e.Message, nil)
	case "validation_error":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, e.Message)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, e.Message).
			Wrap(fmt.Errorf("stripe type=%s code=%s", e.Type, e.Code))
	}
}

// mapDecline traduce decline_code de Stripe a NormalizedError.
func mapDecline(decline, code, msg string) *core.NormalizedError {
	switch decline {
	case "insufficient_funds":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, decline, code, msg)
	case "expired_card":
		return core.NewDecline(core.ErrExpiredCard, Provider, decline, code, msg)
	case "incorrect_cvc":
		return core.NewDecline(core.ErrInvalidCVC, Provider, decline, code, msg)
	case "incorrect_number":
		return core.NewDecline(core.ErrInvalidNumber, Provider, decline, code, msg)
	case "fraudulent":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, decline, code, msg)
	case "do_not_honor":
		return core.NewDecline(core.ErrDoNotHonor, Provider, decline, code, msg)
	case "lost_card":
		return core.NewDecline(core.ErrLostCard, Provider, decline, code, msg)
	case "stolen_card":
		return core.NewDecline(core.ErrStolenCard, Provider, decline, code, msg)
	case "transaction_not_allowed", "transaction_blocked_high_risk":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, decline, code, msg)
	case "processing_error":
		return core.NewDecline(core.ErrProcessingError, Provider, decline, code, msg)
	case "card_velocity_exceeded", "over_limit":
		return core.NewDecline(core.ErrLimitExceeded, Provider, decline, code, msg)
	default:
		return core.NewDecline(core.ErrCardDeclined, Provider, decline, code, msg)
	}
}


