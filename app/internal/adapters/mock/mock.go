// Package mock es un adapter de referencia que implementa core.Gateway sin
// invocar a ningún proveedor real. Sirve para:
//   - Tests del Core, router, cascading y webhook sin red.
//   - Plantilla/copy-paste para escribir adapters reales en Fase 2.
//   - Desarrollo local del SaaS sin credenciales de proveedores.
//
// NO debe usarse en producción. Está registrado en factory.Default bajo el
// ProviderID "mock".
package mock

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
)

func init() {
	// Auto-registro en el registry global para que esté disponible con solo
	// hacer blank import: import _ "github.com/qampu/pop/internal/adapters/mock"
	factory.Default.Register(Provider, Caps, New)
}

// Adapter implementa core.Gateway contra un TenantContext.
type Adapter struct {
	tctx *core.TenantContext
	ops  atomic.Int64
}

// New construye un adapter mock para el TenantContext dado.
func New(tctx *core.TenantContext) (core.Gateway, error) {
	return &Adapter{tctx: tctx}, nil
}

// Capabilities estáticas del mock: soporta todo, en todos lados.
var Caps = core.Capabilities{
	Countries:            nil, // global
	Currencies:           nil, // todas
	Methods: []core.PaymentMethod{
		core.MethodCard, core.MethodPix, core.MethodSPEI, core.MethodPSE,
		core.MethodPagoEfectivo, core.MethodYape, core.MethodPlin,
	},
	SupportsAuthOnly:      true,
	SupportsRefundPartial: true,
	SupportsVaulting:      true,
}

// Provider es el ProviderID del mock.
const Provider core.ProviderID = "mock"

func (a *Adapter) Provider() core.ProviderID      { return Provider }
func (a *Adapter) Capabilities() core.Capabilities { return Caps }

func (a *Adapter) Tokenize(ctx context.Context, in *core.TokenizeRequest) (*core.TokenizeResponse, error) {
	a.ops.Add(1)
	tok := "mock_tok_"
	if in.Card != nil {
		tok += in.Card.Last4
	}
	return &core.TokenizeResponse{
		ProviderToken: tok,
		Vaulted:       true,
		Method:        in.Method,
		Last4:         last4(in),
		Brand:         brand(in),
		ExpiresAt:     ptrTime(time.Now().Add(24 * time.Hour)),
	}, nil
}

func (a *Adapter) Authorize(ctx context.Context, in *core.AuthorizeRequest) (*core.PaymentResult, error) {
	a.ops.Add(1)
	return a.result(in.Reference, in.Amount, in.Method, core.StatusAuthorized), nil
}

func (a *Adapter) Capture(ctx context.Context, in *core.CaptureRequest) (*core.PaymentResult, error) {
	a.ops.Add(1)
	return &core.PaymentResult{
		ID:        "mock_cap_" + in.AuthorizationID,
		Status:    core.StatusCaptured,
		Method:    core.MethodCard,
		Amount:    in.Amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (a *Adapter) Charge(ctx context.Context, in *core.ChargeRequest) (*core.PaymentResult, error) {
	a.ops.Add(1)
	status := core.StatusCaptured
	if !in.Capture {
		status = core.StatusAuthorized
	}
	return a.result(in.Reference, in.Amount, in.Method, status), nil
}

func (a *Adapter) Refund(ctx context.Context, in *core.RefundRequest) (*core.RefundResult, error) {
	a.ops.Add(1)
	return &core.RefundResult{
		ID:        "mock_ref_" + in.PaymentID,
		PaymentID: in.PaymentID,
		Status:    core.StatusRefunded,
		Amount:    in.Amount,
		Provider:  Provider,
		TenantID:  a.tctx.TenantID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (a *Adapter) Void(ctx context.Context, in *core.VoidRequest) (*core.PaymentResult, error) {
	a.ops.Add(1)
	return &core.PaymentResult{
		ID:        "mock_void_" + in.AuthorizationID,
		Status:    core.StatusVoided,
		Amount:    core.Money{},
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (a *Adapter) result(ref string, amount core.Money, method core.PaymentMethod, status core.PaymentStatus) *core.PaymentResult {
	return &core.PaymentResult{
		ID:        "mock_pay_" + ref,
		Status:    status,
		Method:    method,
		Amount:    amount,
		Provider:  Provider,
		Country:   a.tctx.Country,
		TenantID:  a.tctx.TenantID,
		Reference: ref,
		CreatedAt: time.Now().UTC(),
	}
}

func last4(in *core.TokenizeRequest) string {
	if in.Card != nil {
		return in.Card.Last4
	}
	return ""
}

func brand(in *core.TokenizeRequest) string {
	if in.Card != nil {
		return in.Card.Brand
	}
	return ""
}

func ptrTime(t time.Time) *time.Time { return &t }
