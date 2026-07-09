// Package kushki es el adapter real para la pasarela Kushki.
//
// Implementa core.Gateway invocando a la REST API de Kushki (v1) usando
// únicamente la stdlib de Go (sin SDK externo). Mantiene el módulo sin
// dependencias adicionales y el contrato explícito con el proveedor.
//
// Kushki usa autenticación Bearer con el private merchant ID del integrador.
// Los montos viajan en la unidad mínima de la moneda (cents) — coincide con
// el formato interno del SDK (core.Money.Amount), así que no hay conversión.
//
// El adapter se construye por petición con un TenantContext (credenciales
// desencriptadas del tenant). No mantiene estado global ni cachea
// credenciales: cada request construye su propio HTTP client.
//
// Registro:
//
//	import _ "github.com/qampu/pop/internal/adapters/kushki"
//
// Esto pobla factory.Default y webhook.Default en init().
package kushki

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

// Provider es el ProviderID canónico de Kushki.
const Provider core.ProviderID = core.ProviderKushki

// Caps describe las capabilities estáticas de Kushki para el router.
// Kushki está disponible en Ecuador y Colombia, soporta auth-only, refunds
// parciales y vaulting (customers + cards).
var Caps = core.Capabilities{
	Countries: []string{
		"EC", "CO",
	},
	Currencies: []string{
		"USD", "COP",
	},
	Methods: []core.PaymentMethod{
		core.MethodCard,
	},
	SupportsAuthOnly:      true,
	SupportsRefundPartial: true,
	SupportsVaulting:      true,
}

// Base URLs (override vía TenantContext.EndpointURL para proxies/sandbox).
const (
	defaultBaseURL = "https://api.kushkipagos.com"
	defaultTimeout = 30 * time.Second
)

func init() {
	factory.Default.Register(Provider, Caps, New)
	webhook.Default.Register(&webhook.WebhookHandler{
		Provider:  Provider,
		Verifier:  &kushkiVerifier{},
		Normalize: &kushkiNormalizer{},
	})
}

// Adapter implementa core.Gateway contra Kushki.
type Adapter struct {
	tctx *core.TenantContext
	base string
	hc   *http.Client
}

// New construye un adapter Kushki para el TenantContext dado.
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

// doJSON envía una request JSON a Kushki con auth Bearer (private merchant ID).
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
	req.Header.Set("Private-Merchant-Id", a.tctx.Secret)
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
			fmt.Sprintf("kushki %d: %s", resp.StatusCode, truncate(respBody)), nil)
	}
	if resp.StatusCode == 429 {
		return core.NewTransient(core.ErrRateLimited, Provider,
			"kushki rate limited", nil)
	}
	if resp.StatusCode >= 400 {
		return parseKushkiError(respBody)
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

// Tokenize crea un token de tarjeta en Kushki a partir del token del frontend.
// Kushki permite tokenizar en el frontend (SDK JS) y reusar el token; este método
// re-tokeniza desde el backend cuando el tenant necesita vaulting explícito.
func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	if in.Method != core.MethodCard || in.Card == nil {
		return nil, core.NewError(core.ErrUnsupportedMethod, core.CategoryUnsupported, Provider,
			"kushki tokenize only supports card method")
	}

	payload := map[string]any{
		"token": in.Card.Token,
		"card": map[string]string{
			"last4": in.Card.Last4,
			"brand": in.Card.Brand,
		},
	}
	if in.Buyer != nil && in.Buyer.Email != "" {
		payload["email"] = in.Buyer.Email
	}

	var tok kushkiToken
	if err := a.doJSON(ctx, http.MethodPost, "tokenize", in.Card.Token, "/card/v1/tokens", payload, &tok); err != nil {
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

// Authorize crea un charge con capture=false (auth-only).
func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	payload := a.chargePayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, false)
	var charge kushkiCharge
	if err := a.doJSON(ctx, http.MethodPost, "authorize", in.Reference, "/card/v1/charges", payload, &charge); err != nil {
		return nil, err
	}
	return a.toResult(&charge, in.Reference, in.Amount, in.Method), nil
}

// Charge crea un charge con capture=true (auth+capture).
func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	payload := a.chargePayload(in.Reference, in.Amount, in.Method, in.ProviderToken, in.Buyer, in.Description, in.Metadata, in.Capture)
	var charge kushkiCharge
	if err := a.doJSON(ctx, http.MethodPost, "charge", in.Reference, "/card/v1/charges", payload, &charge); err != nil {
		return nil, err
	}
	return a.toResult(&charge, in.Reference, in.Amount, in.Method), nil
}

// Capture confirma la captura de un charge previamente autorizado.
// Kushki usa POST /card/v1/capture con el ticket number.
func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	payload := map[string]any{
		"ticketNumber": in.AuthorizationID,
		"amount":       float64(in.Amount.Amount) / 100.0,
		"currency":     strings.ToUpper(in.Amount.Currency),
	}
	var charge kushkiCharge
	if err := a.doJSON(ctx, http.MethodPost, "capture", in.AuthorizationID, "/card/v1/capture", payload, &charge); err != nil {
		return nil, err
	}
	return a.toResult(&charge, in.AuthorizationID, in.Amount, core.MethodCard), nil
}

// Void cancela un charge que está en state authorized/pendiente.
// Kushki usa POST /card/v1/void con el ticket number.
func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	payload := map[string]any{
		"ticketNumber": in.AuthorizationID,
		"reason":       in.Reason,
	}
	var charge kushkiCharge
	if err := a.doJSON(ctx, http.MethodPost, "void", in.AuthorizationID, "/card/v1/void", payload, &charge); err != nil {
		return nil, err
	}
	return a.toResult(&charge, in.AuthorizationID, core.Money{}, core.MethodCard), nil
}

// Refund emite un reembolso total o parcial sobre un charge.
// Kushki usa POST /card/v1/refunds con el ticket number.
func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	payload := map[string]any{
		"ticketNumber": in.PaymentID,
		"amount":       float64(in.Amount.Amount) / 100.0,
		"currency":     strings.ToUpper(in.Amount.Currency),
		"reason":       mapRefundReason(in.Reason),
	}
	var r kushkiRefund
	if err := a.doJSON(ctx, http.MethodPost, "refund", in.PaymentID, "/card/v1/refunds", payload, &r); err != nil {
		return nil, err
	}
	return &core.RefundResult{
		ID:        r.RefundTicketNumber,
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

func (a *Adapter) chargePayload(
	ref string, amount core.Money, method core.PaymentMethod, token string,
	buyer *core.Buyer, desc string, meta map[string]string, capture bool,
) map[string]any {
	out := map[string]any{
		"amount":         float64(amount.Amount) / 100.0,
		"currency":       strings.ToUpper(amount.Currency),
		"capture":        capture,
		"metadata":       mergeMeta(meta, map[string]string{"reference": ref}),
		"responseType":   "FULL_SYNC",
	}
	if desc != "" {
		out["description"] = desc
	}
	if token != "" {
		out["token"] = token
	}
	if buyer != nil {
		b := map[string]any{}
		if buyer.Email != "" {
			b["email"] = buyer.Email
		}
		if buyer.FirstName != "" || buyer.LastName != "" {
			b["firstName"] = buyer.FirstName
			b["lastName"] = buyer.LastName
		}
		if buyer.DocType != "" {
			b["identification"] = map[string]string{
				"type":   buyer.DocType,
				"number": buyer.DocNumber,
			}
		}
		out["buyer"] = b
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

func (a *Adapter) toResult(charge *kushkiCharge, ref string, amount core.Money, method core.PaymentMethod) *core.PaymentResult {
	res := &core.PaymentResult{
		ID:        charge.TicketNumber,
		Status:    mapChargeStatus(charge.Status),
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: parseKushkiDate(charge.CreatedAt),
		Raw:       charge.Raw,
	}
	if charge.Details != nil && charge.Details.RedirectURL != "" {
		// APMs (cash/transfer) generan un redirect URL.
		res.NextAction = &core.NextAction{
			Type:        core.NextActionRedirect,
			RedirectURL: charge.Details.RedirectURL,
		}
	}
	return res
}

// --- mappers ---

func mapChargeStatus(s string) core.PaymentStatus {
	switch s {
	case "CAPTURED":
		return core.StatusCaptured
	case "AUTHORIZED":
		return core.StatusAuthorized
	case "PENDING":
		return core.StatusPending
	case "DECLINED":
		return core.StatusFailed
	case "VOIDED":
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
	case "APPROVED":
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

func parseKushkiDate(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	// Kushki usa ISO 8601: 2025-01-02T15:04:05.000Z
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

// --- tipos crudos de Kushki (subset) ---

type kushkiCharge struct {
	TicketNumber string        `json:"ticketNumber"`
	Status       string        `json:"status"`
	Amount       float64       `json:"amount"`
	Currency     string        `json:"currency"`
	CreatedAt    string        `json:"createdAt"`
	Details      *struct {
		RedirectURL string `json:"redirectUrl"`
	} `json:"details,omitempty"`
	Raw map[string]any `json:"-"`
}

// UnmarshalJSON captura el body crudo para Raw (debugging/auditoría).
func (c *kushkiCharge) UnmarshalJSON(b []byte) error {
	type alias kushkiCharge
	var tmp alias
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*c = kushkiCharge(tmp)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err == nil {
		c.Raw = raw
	}
	return nil
}

type kushkiToken struct {
	Token string `json:"token"`
}

type kushkiRefund struct {
	RefundTicketNumber string  `json:"refundTicketNumber"`
	Status             string  `json:"status"`
	Amount             float64 `json:"amount"`
}

// parseKushkiError traduce un error JSON de Kushki a NormalizedError.
//
// Formato Kushki: {"code": "...", "message": "...", "details": "..."}
func parseKushkiError(body []byte) error {
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider,
			fmt.Sprintf("unparseable kushki error: %s", truncate(body)))
	}

	msg := env.Message
	if msg == "" {
		msg = env.Details
	}

	// Errores de auth del tenant.
	if env.Code == "K001" || env.Code == "K002" {
		return core.NewError(core.ErrInvalidCredentials, core.CategoryAuth, Provider, msg)
	}

	// Mapeo por código de error.
	switch env.Code {
	case "K003":
		return core.NewError(core.ErrInvalidRequest, core.CategoryValidation, Provider, msg)
	case "K004":
		return core.NewDecline(core.ErrCardDeclined, Provider, env.Code, env.Code, msg)
	case "K005":
		return core.NewDecline(core.ErrInsufficientFunds, Provider, env.Code, env.Code, msg)
	case "K006":
		return core.NewDecline(core.ErrExpiredCard, Provider, env.Code, env.Code, msg)
	case "K007":
		return core.NewDecline(core.ErrInvalidCVC, Provider, env.Code, env.Code, msg)
	case "K008":
		return core.NewDecline(core.ErrInvalidNumber, Provider, env.Code, env.Code, msg)
	case "K009":
		return core.NewDecline(core.ErrSuspectedFraud, Provider, env.Code, env.Code, msg)
	case "K010":
		return core.NewDecline(core.ErrDoNotHonor, Provider, env.Code, env.Code, msg)
	case "K011":
		return core.NewDecline(core.ErrProcessingError, Provider, env.Code, env.Code, msg)
	case "K012":
		return core.NewTransient(core.ErrRateLimited, Provider, msg, nil)
	default:
		return core.NewError(core.ErrUnknown, core.CategoryGateway, Provider, msg).
			Wrap(fmt.Errorf("kushki code=%s", env.Code))
	}
}
