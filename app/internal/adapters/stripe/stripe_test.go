package stripe

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
		TenantID: "tnt_1",
		Provider: Provider,
		Country:  "US",
		Mode:     core.EnvTest,
		Secret:   "sk_test_xxx",
	}
	gw, err := New(tctx)
	if err != nil {
		t.Fatal(err)
	}
	a := gw.(*Adapter)
	a.base = base
	return a
}

// fakeStripe levanta un server que responde según el path recibido.
type fakeStripe struct {
	t        *testing.T
	lastReq  *http.Request
	lastBody string
	resp     string
	status   int
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func newFakeServer(t *testing.T) (*httptest.Server, *fakeStripe) {
	f := &fakeStripe{t: t}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return s, f
}

// --- Tests ---

func TestChargeHappyPath(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"pi_123","status":"succeeded","amount":1999,"currency":"usd","created":1700000000}`
	a := newAdapter(t, s.URL)

	res, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_42",
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "pm_xyz",
		Capture:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", res.Status)
	}
	if res.ID != "pi_123" {
		t.Errorf("expected pi_123, got %s", res.ID)
	}
	if res.Provider != Provider {
		t.Errorf("expected stripe provider, got %s", res.Provider)
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
	// Verifica que el form lleva amount y currency.
	if !strings.Contains(f.lastBody, "amount=1999") {
		t.Errorf("expected amount=1999 in body, got %s", f.lastBody)
	}
	if !strings.Contains(f.lastBody, "currency=usd") {
		t.Errorf("expected currency=usd in body, got %s", f.lastBody)
	}
}

func TestAuthorizeCaptureFlow(t *testing.T) {
	s, f := newFakeServer(t)
	a := newAdapter(t, s.URL)

	// Authorize → requires_capture.
	f.resp = `{"id":"pi_auth","status":"requires_capture","amount":5000,"currency":"usd","created":1700000000}`
	auth, err := a.Authorize(context.Background(), &core.AuthorizeRequest{
		Reference:     "order_43",
		Amount:        core.Money{Amount: 5000, Currency: "USD"},
		Method:        core.MethodCard,
		ProviderToken: "pm_xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Status != core.StatusAuthorized {
		t.Errorf("expected authorized, got %s", auth.Status)
	}
	if !strings.Contains(f.lastBody, "capture_method=manual") {
		t.Errorf("expected capture_method=manual, got %s", f.lastBody)
	}

	// Capture.
	f.resp = `{"id":"pi_auth","status":"succeeded","amount":5000,"currency":"usd","created":1700000000}`
	cap, err := a.Capture(context.Background(), &core.CaptureRequest{
		AuthorizationID: "pi_auth",
		Amount:          core.Money{Amount: 5000, Currency: "USD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", cap.Status)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/payment_intents/pi_auth/capture") {
		t.Errorf("expected capture path, got %s", f.lastReq.URL.Path)
	}
}

func TestVoid(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"pi_auth","status":"canceled","amount":5000,"currency":"usd","created":1700000000}`
	a := newAdapter(t, s.URL)

	res, err := a.Void(context.Background(), &core.VoidRequest{
		AuthorizationID: "pi_auth",
		Reason:          "requested_by_customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusVoided {
		t.Errorf("expected voided, got %s", res.Status)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/cancel") {
		t.Errorf("expected cancel path, got %s", f.lastReq.URL.Path)
	}
}

func TestRefund(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"re_123","amount":1000,"currency":"usd","status":"succeeded","created":1700000000}`
	a := newAdapter(t, s.URL)

	res, err := a.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "pi_123",
		Amount:    core.Money{Amount: 1000, Currency: "USD"},
		Reason:    core.RefundRequestedByCustomer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusRefunded {
		t.Errorf("expected refunded, got %s", res.Status)
	}
	if res.ID != "re_123" {
		t.Errorf("expected re_123, got %s", res.ID)
	}
	if !strings.Contains(f.lastBody, "payment_intent=pi_123") {
		t.Errorf("expected payment_intent=pi_123, got %s", f.lastBody)
	}
	if !strings.Contains(f.lastBody, "reason=requested_by_customer") {
		t.Errorf("expected reason=requested_by_customer, got %s", f.lastBody)
	}
}

func TestTokenize(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"pm_abc","card":{"last4":"4242","brand":"visa"}}`
	a := newAdapter(t, s.URL)

	out, err := a.Tokenize(context.Background(), &core.TokenizeRequest{
		Method: core.MethodCard,
		Card:   &core.CardToken{Token: "tok_front", Last4: "4242", Brand: "visa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderToken != "pm_abc" {
		t.Errorf("expected pm_abc, got %s", out.ProviderToken)
	}
	if out.Last4 != "4242" {
		t.Errorf("expected last4 4242, got %s", out.Last4)
	}
	if !strings.Contains(f.lastBody, "card%5Btoken%5D=tok_front") {
		t.Errorf("expected card[token]=tok_front, got %s", f.lastBody)
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
	f.status = 402
	f.resp = `{"error":{"type":"card_error","code":"card_declined","decline_code":"insufficient_funds","message":"Your card has insufficient funds."}}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
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
	if ne.Code != core.ErrInsufficientFunds {
		t.Errorf("expected ErrInsufficientFunds, got %s", ne.Code)
	}
	if ne.Retryable {
		t.Error("decline should not be retryable")
	}
	if ne.DeclineCode != "insufficient_funds" {
		t.Errorf("expected decline_code insufficient_funds, got %s", ne.DeclineCode)
	}
}

func TestRateLimitRetryable(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 429
	f.resp = `{"error":{"type":"rate_limit_error","message":"Too many requests"}}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
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
	f.resp = `{"error":{"type":"api_error","message":"Service unavailable"}}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
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
	f.resp = `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Invalid API Key provided"}}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
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
		Amount:        core.Money{Amount: 1999, Currency: "USD"},
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
	body := `{"id":"evt_1","type":"payment_intent.succeeded","created":1700000000,"data":{"object":{"id":"pi_123","status":"succeeded","amount":1999,"currency":"usd"}}}`
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", ts, body)))
	sig := hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))
	req.Header.Set("X-Stripe-Webhook-Secret", secret)
	req.Header.Set("X-Stripe-Tenant", "tnt_1")

	v := &stripeVerifier{}
	tenantID, err := v.Verify(context.Background(), req.Header, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if tenantID != "tnt_1" {
		t.Errorf("expected tnt_1, got %s", tenantID)
	}
}

func TestWebhookVerifyBadSignature(t *testing.T) {
	secret := "whsec_test"
	body := `{"id":"evt_1","type":"payment_intent.succeeded"}`
	ts := time.Now().Unix()

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=deadbeef", ts))
	req.Header.Set("X-Stripe-Webhook-Secret", secret)

	v := &stripeVerifier{}
	if _, err := v.Verify(context.Background(), req.Header, []byte(body)); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestWebhookVerifyStaleTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := `{"id":"evt_1"}`
	ts := time.Now().Add(-10 * time.Minute).Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", ts, body)))
	sig := hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))
	req.Header.Set("X-Stripe-Webhook-Secret", secret)

	v := &stripeVerifier{}
	if _, err := v.Verify(context.Background(), req.Header, []byte(body)); err == nil {
		t.Fatal("expected stale timestamp error")
	}
}

func TestWebhookNormalize(t *testing.T) {
	body := `{"id":"evt_1","type":"payment_intent.succeeded","created":1700000000,"data":{"object":{"id":"pi_123","status":"succeeded","amount":1999,"currency":"usd","description":"order_42","metadata":{"reference":"order_42"}}}}`
	tctx := &core.TenantContext{TenantID: "tnt_1", Provider: Provider, Country: "US", Mode: core.EnvTest, Secret: "x"}

	n := &stripeNormalizer{}
	ev, err := n.Normalize(context.Background(), tctx, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.captured" {
		t.Errorf("expected payment.captured, got %s", ev.Type)
	}
	if ev.PaymentID != "pi_123" {
		t.Errorf("expected pi_123, got %s", ev.PaymentID)
	}
	if ev.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", ev.Status)
	}
	if ev.Amount.Amount != 1999 || ev.Amount.Currency != "USD" {
		t.Errorf("amount mismatch: %+v", ev.Amount)
	}
	if ev.Reference != "order_42" {
		t.Errorf("expected order_42, got %s", ev.Reference)
	}
	if ev.ProviderEventID != "evt_1" {
		t.Errorf("expected evt_1, got %s", ev.ProviderEventID)
	}
}

func TestWebhookNormalizeFailedEvent(t *testing.T) {
	body := `{"id":"evt_2","type":"payment_intent.payment_failed","created":1700000000,"data":{"object":{"id":"pi_456","status":"requires_payment_method","amount":1999,"currency":"usd"}}}`
	tctx := &core.TenantContext{TenantID: "tnt_1", Provider: Provider, Country: "US", Mode: core.EnvTest, Secret: "x"}

	n := &stripeNormalizer{}
	ev, err := n.Normalize(context.Background(), tctx, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.failed" {
		t.Errorf("expected payment.failed, got %s", ev.Type)
	}
	if ev.Status != core.StatusFailed {
		t.Errorf("expected failed, got %s", ev.Status)
	}
}

// --- Test de registro en factory ---

func TestStripeRegisteredInFactory(t *testing.T) {
	// El init() del package registra el adapter en factory.Default.
	// Verificamos que las capabilities están presentes.
	caps, ok := factory.Default.Capabilities(Provider)
	if !ok {
		t.Fatal("stripe not registered in factory.Default")
	}
	if !caps.SupportsAuthOnly {
		t.Error("stripe should support auth-only")
	}
	if !caps.SupportsRefundPartial {
		t.Error("stripe should support partial refund")
	}
}

// --- Test de mappers ---

func TestMapPIStatus(t *testing.T) {
	cases := map[string]core.PaymentStatus{
		"succeeded":              core.StatusCaptured,
		"requires_capture":       core.StatusAuthorized,
		"requires_action":        core.StatusPending,
		"processing":             core.StatusPending,
		"canceled":               core.StatusVoided,
		"requires_payment_method": core.StatusFailed,
		"unknown_weird":          core.StatusFailed,
	}
	for in, want := range cases {
		if got := mapPIStatus(in); got != want {
			t.Errorf("mapPIStatus(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMapEventType(t *testing.T) {
	cases := map[string]webhook.EventType{
		"payment_intent.succeeded":                 webhook.EventPaymentCaptured,
		"payment_intent.payment_failed":            webhook.EventPaymentFailed,
		"payment_intent.canceled":                  webhook.EventPaymentVoided,
		"payment_intent.requires_action":           webhook.EventPaymentPending,
		"payment_intent.amount_capturable_updated": webhook.EventPaymentAuthorized,
		"charge.refunded":                          webhook.EventRefundCompleted,
		"charge.dispute.created":                   webhook.EventDisputeOpened,
		"charge.dispute.closed":                    webhook.EventDisputeResolved,
	}
	for in, want := range cases {
		got := mapEventType(in)
		if got != want {
			t.Errorf("mapEventType(%q) = %s, want %s", in, got, want)
		}
	}
}
