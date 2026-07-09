package niubiz

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/webhook"
)

// newAdapter construye un Adapter apuntando a un server de test.
func newAdapter(t *testing.T, base string) *Adapter {
	tctx := &core.TenantContext{
		TenantID:      "tnt_1",
		Provider:      Provider,
		Country:       "PE",
		Mode:          core.EnvTest,
		Secret:        "sk_test_xxx",
		WebhookSecret: "whsec_test",
	}
	gw, err := New(tctx)
	if err != nil {
		t.Fatal(err)
	}
	a := gw.(*Adapter)
	a.base = base
	return a
}

// fakeNiubiz levanta un server que responde según el path recibido.
type fakeNiubiz struct {
	t        *testing.T
	lastReq  *http.Request
	lastBody string
	resp     string
	status   int
}

func (f *fakeNiubiz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.lastReq = r
	f.lastBody = string(body)
	if f.status == 0 {
		f.status = 200
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	w.Write([]byte(f.resp))
}

func newFakeServer(t *testing.T) (*httptest.Server, *fakeNiubiz) {
	f := &fakeNiubiz{t: t}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return s, f
}

// --- Tests ---

func TestChargeHappyPath(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"pay_123","status":"SUCCESS","amount":199.90,"currency":"PEN","created_at":"2025-01-02T15:04:05Z"}`
	a := newAdapter(t, s.URL)

	res, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_42",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_xyz",
		Capture:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", res.Status)
	}
	if res.ID != "pay_123" {
		t.Errorf("expected pay_123, got %s", res.ID)
	}
	if res.Provider != Provider {
		t.Errorf("expected niubiz provider, got %s", res.Provider)
	}
	if res.TenantID != "tnt_1" {
		t.Errorf("tenant isolation broken: %s", res.TenantID)
	}
	// Verifica auth Basic con la secret key.
	user, _, ok := f.lastReq.BasicAuth()
	if !ok || user != "sk_test_xxx" {
		t.Errorf("expected basic auth sk_test_xxx, got %q", user)
	}
	// Verifica Idempotency-Key.
	if f.lastReq.Header.Get("Idempotency-Key") == "" {
		t.Error("missing Idempotency-Key header")
	}
	// Verifica que el JSON lleva amount y currency.
	if !strings.Contains(f.lastBody, "amount") {
		t.Errorf("expected amount in body, got %s", f.lastBody)
	}
	if !strings.Contains(f.lastBody, "PEN") {
		t.Errorf("expected PEN in body, got %s", f.lastBody)
	}
}

func TestAuthorizeCaptureFlow(t *testing.T) {
	s, f := newFakeServer(t)
	a := newAdapter(t, s.URL)

	// Authorize → AUTHORIZED.
	f.resp = `{"id":"pay_auth","status":"AUTHORIZED","amount":50.00,"currency":"PEN","created_at":"2025-01-02T15:04:05Z"}`
	auth, err := a.Authorize(context.Background(), &core.AuthorizeRequest{
		Reference:     "order_43",
		Amount:        core.Money{Amount: 5000, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Status != core.StatusAuthorized {
		t.Errorf("expected authorized, got %s", auth.Status)
	}
	if !strings.Contains(f.lastBody, "capture") {
		t.Errorf("expected capture in body, got %s", f.lastBody)
	}

	// Capture.
	f.resp = `{"id":"pay_auth","status":"SUCCESS","amount":50.00,"currency":"PEN","created_at":"2025-01-02T15:04:05Z"}`
	cap, err := a.Capture(context.Background(), &core.CaptureRequest{
		AuthorizationID: "pay_auth",
		Amount:          core.Money{Amount: 5000, Currency: "PEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", cap.Status)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/capture") {
		t.Errorf("expected capture path, got %s", f.lastReq.URL.Path)
	}
}

func TestVoid(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"pay_auth","status":"CANCELLED","amount":50.00,"currency":"PEN","created_at":"2025-01-02T15:04:05Z"}`
	a := newAdapter(t, s.URL)

	res, err := a.Void(context.Background(), &core.VoidRequest{
		AuthorizationID: "pay_auth",
		Reason:          "requested_by_customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusVoided {
		t.Errorf("expected voided, got %s", res.Status)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/void") {
		t.Errorf("expected void path, got %s", f.lastReq.URL.Path)
	}
}

func TestRefund(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"ref_123","status":"SUCCESS","amount":10.00,"currency":"PEN"}`
	a := newAdapter(t, s.URL)

	res, err := a.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "pay_123",
		Amount:    core.Money{Amount: 1000, Currency: "PEN"},
		Reason:    core.RefundRequestedByCustomer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusRefunded {
		t.Errorf("expected refunded, got %s", res.Status)
	}
	if res.ID != "ref_123" {
		t.Errorf("expected ref_123, got %s", res.ID)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/refunds") {
		t.Errorf("expected refunds path, got %s", f.lastReq.URL.Path)
	}
}

func TestTokenize(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"token":"tok_abc"}`
	a := newAdapter(t, s.URL)

	out, err := a.Tokenize(context.Background(), &core.TokenizeRequest{
		Method: core.MethodCard,
		Card:   &core.CardToken{Token: "tok_front", Last4: "4242", Brand: "visa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderToken != "tok_abc" {
		t.Errorf("expected tok_abc, got %s", out.ProviderToken)
	}
	if out.Last4 != "4242" {
		t.Errorf("expected last4 4242, got %s", out.Last4)
	}
}

func TestTokenizeUnsupportedMethod(t *testing.T) {
	s, _ := newFakeServer(t)
	a := newAdapter(t, s.URL)

	_, err := a.Tokenize(context.Background(), &core.TokenizeRequest{
		Method: core.MethodPix,
	})
	if err == nil {
		t.Fatal("expected unsupported method error")
	}
	ne, ok := err.(*core.NormalizedError)
	if !ok || ne.Code != core.ErrUnsupportedMethod {
		t.Errorf("expected ErrUnsupportedMethod, got %v", err)
	}
}

// --- Tests de errores ---

func TestCardDeclinedError(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 400
	f.resp = `{"code":"1001","message":"Card declined"}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "pm_x",
		Capture:       true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	ne, ok := err.(*core.NormalizedError)
	if !ok {
		t.Fatalf("expected NormalizedError, got %T", err)
	}
	if ne.Code != core.ErrCardDeclined {
		t.Errorf("expected ErrCardDeclined, got %s", ne.Code)
	}
	if ne.Retryable {
		t.Error("decline should not be retryable")
	}
}

func TestRateLimitRetryable(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 429
	f.resp = `{"code":"1009","message":"Too many requests"}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "pm_x",
		Capture:       true,
	})
	ne, ok := err.(*core.NormalizedError)
	if !ok {
		t.Fatalf("expected NormalizedError, got %T", err)
	}
	if !ne.Retryable {
		t.Error("rate limit should be retryable")
	}
	if ne.Code != core.ErrRateLimited {
		t.Errorf("expected ErrRateLimited, got %s", ne.Code)
	}
}

func TestProviderDownRetryable(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 503
	f.resp = `{"code":"500","message":"Service unavailable"}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "pm_x",
		Capture:       true,
	})
	ne, ok := err.(*core.NormalizedError)
	if !ok {
		t.Fatalf("expected NormalizedError, got %T", err)
	}
	if !ne.Retryable {
		t.Error("5xx should be retryable")
	}
	if ne.Code != core.ErrProviderDown {
		t.Errorf("expected ErrProviderDown, got %s", ne.Code)
	}
}

func TestInvalidCredentialsError(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 401
	f.resp = `{"code":"401","message":"Invalid API Key"}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "pm_x",
		Capture:       true,
	})
	ne, ok := err.(*core.NormalizedError)
	if !ok {
		t.Fatalf("expected NormalizedError, got %T", err)
	}
	if ne.Code != core.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %s", ne.Code)
	}
}

func TestNetworkErrorRetryable(t *testing.T) {
	// Server cerrado → error de red.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.Close()
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 19990, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "pm_x",
		Capture:       true,
	})
	ne, ok := err.(*core.NormalizedError)
	if !ok {
		t.Fatalf("expected NormalizedError, got %T", err)
	}
	if !ne.Retryable {
		t.Error("network error should be retryable")
	}
}

// --- Tests de webhook ---

func TestWebhookVerifyValid(t *testing.T) {
	secret := "whsec_test"
	body := `{"id":"evt_1","type":"payment.success","created_at":1700000000,"data":{"id":"pay_123","status":"SUCCESS","amount":199.90,"currency":"PEN","order_id":"order_42"}}`
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d", ts)))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/niubiz", strings.NewReader(body))
	req.Header.Set("X-Signature", fmt.Sprintf("hmac-sha256=%s", sig))

	v := &niubizVerifier{}
	err := v.Verify([]byte(body), req.Header, secret)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebhookVerifyBadSignature(t *testing.T) {
	secret := "whsec_test"
	body := `{"id":"evt_1","type":"payment.success"}`

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/niubiz", strings.NewReader(body))
	req.Header.Set("X-Signature", "hmac-sha256=deadbeef")

	v := &niubizVerifier{}
	if err := v.Verify([]byte(body), req.Header, secret); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestWebhookNormalize(t *testing.T) {
	body := `{"id":"evt_1","type":"payment.success","created_at":1700000000,"data":{"id":"pay_123","status":"SUCCESS","amount":199.90,"currency":"PEN","order_id":"order_42","created_at":1700000000}}`

	n := &niubizNormalizer{}
	ev, err := n.Normalize([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.captured" {
		t.Errorf("expected payment.captured, got %s", ev.Type)
	}
	if ev.Provider != Provider {
		t.Errorf("expected niubiz provider, got %s", ev.Provider)
	}
}

func TestWebhookNormalizeFailedEvent(t *testing.T) {
	body := `{"id":"evt_2","type":"payment.failed","created_at":1700000000,"data":{"id":"pay_456","status":"REJECTED","amount":199.90,"currency":"PEN","order_id":"order_43"}}`

	n := &niubizNormalizer{}
	ev, err := n.Normalize([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.failed" {
		t.Errorf("expected payment.failed, got %s", ev.Type)
	}
}

// --- Test de registro en factory ---

func TestNiubizRegisteredInFactory(t *testing.T) {
	// El init() del package registra el adapter en factory.Default.
	// Verificamos que las capabilities están presentes.
	caps, ok := factory.Default.Capabilities(Provider)
	if !ok {
		t.Fatal("niubiz not registered in factory.Default")
	}
	if !caps.SupportsAuthOnly {
		t.Error("niubiz should support auth-only")
	}
	if !caps.SupportsRefundPartial {
		t.Error("niubiz should support partial refund")
	}
}

// --- Test de mappers ---

func TestMapPaymentStatus(t *testing.T) {
	cases := map[string]core.PaymentStatus{
		"SUCCESS":           core.StatusCaptured,
		"AUTHORIZED":        core.StatusAuthorized,
		"PENDING":           core.StatusPending,
		"REJECTED":          core.StatusFailed,
		"CANCELLED":         core.StatusVoided,
		"REFUNDED":          core.StatusRefunded,
		"PARTIALLY_REFUNDED": core.StatusPartiallyRefunded,
		"unknown_weird":     core.StatusFailed,
	}
	for in, want := range cases {
		if got := mapPaymentStatus(in); got != want {
			t.Errorf("mapPaymentStatus(%q) = %s, want %s", in, got, want)
		}
	}
}
