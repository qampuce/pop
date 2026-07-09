package mercadopago

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
		Country:  "PE",
		Mode:     core.EnvTest,
		Secret:   "TEST-xxx",
	}
	gw, err := New(tctx)
	if err != nil {
		t.Fatal(err)
	}
	a := gw.(*Adapter)
	a.base = base
	return a
}

// fakeMP levanta un server que responde según el path recibido.
type fakeMP struct {
	t        *testing.T
	lastReq  *http.Request
	lastBody string
	resp     string
	status   int
}

func (f *fakeMP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func newFakeServer(t *testing.T) (*httptest.Server, *fakeMP) {
	f := &fakeMP{t: t}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return s, f
}

// --- Tests ---

func TestChargeHappyPath(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":123456789,"status":"approved","status_detail":"accredited","transaction_amount":199.90,"currency_id":"PEN","date_created":"2026-01-02T15:04:05.000-03:00","payment_method_id":"visa"}`
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
	if res.ID != "123456789" {
		t.Errorf("expected 123456789, got %s", res.ID)
	}
	if res.Provider != Provider {
		t.Errorf("expected mercadopago provider, got %s", res.Provider)
	}
	if res.TenantID != "tnt_1" {
		t.Errorf("tenant isolation broken: %s", res.TenantID)
	}
	// Verifica auth Bearer con el access_token.
	auth := f.lastReq.Header.Get("Authorization")
	if auth != "Bearer TEST-xxx" {
		t.Errorf("expected Bearer TEST-xxx, got %q", auth)
	}
	// Verifica Idempotency-Key.
	if f.lastReq.Header.Get("Idempotency-Key") == "" {
		t.Error("missing Idempotency-Key header")
	}
	// Verifica que el body lleva transaction_amount y capture.
	if !strings.Contains(f.lastBody, `"transaction_amount":199.9`) {
		t.Errorf("expected transaction_amount=199.9 in body, got %s", f.lastBody)
	}
	if !strings.Contains(f.lastBody, `"capture":true`) {
		t.Errorf("expected capture=true in body, got %s", f.lastBody)
	}
}

func TestAuthorizeCaptureFlow(t *testing.T) {
	s, f := newFakeServer(t)
	a := newAdapter(t, s.URL)

	// Authorize → authorized.
	f.resp = `{"id":100,"status":"authorized","status_detail":"pending_capture","transaction_amount":50.00,"currency_id":"PEN","date_created":"2026-01-02T15:04:05.000-03:00"}`
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
	if !strings.Contains(f.lastBody, `"capture":false`) {
		t.Errorf("expected capture=false, got %s", f.lastBody)
	}

	// Capture.
	f.resp = `{"id":100,"status":"approved","status_detail":"accredited","transaction_amount":50.00,"currency_id":"PEN","date_created":"2026-01-02T15:04:05.000-03:00"}`
	cap, err := a.Capture(context.Background(), &core.CaptureRequest{
		AuthorizationID: "100",
		Amount:          core.Money{Amount: 5000, Currency: "PEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.Status != core.StatusCaptured {
		t.Errorf("expected captured, got %s", cap.Status)
	}
	if f.lastReq.Method != http.MethodPut {
		t.Errorf("expected PUT for capture, got %s", f.lastReq.Method)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/v1/payments/100") {
		t.Errorf("expected /v1/payments/100 path, got %s", f.lastReq.URL.Path)
	}
	if !strings.Contains(f.lastBody, `"capture":true`) {
		t.Errorf("expected capture=true in body, got %s", f.lastBody)
	}
}

func TestVoid(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":100,"status":"cancelled","status_detail":"expired","transaction_amount":50.00,"currency_id":"PEN","date_created":"2026-01-02T15:04:05.000-03:00"}`
	a := newAdapter(t, s.URL)

	res, err := a.Void(context.Background(), &core.VoidRequest{
		AuthorizationID: "100",
		Reason:          "requested_by_customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusVoided {
		t.Errorf("expected voided, got %s", res.Status)
	}
	if f.lastReq.Method != http.MethodPut {
		t.Errorf("expected PUT for void, got %s", f.lastReq.Method)
	}
	if !strings.Contains(f.lastBody, `"status":"cancelled"`) {
		t.Errorf("expected status=cancelled in body, got %s", f.lastBody)
	}
}

func TestRefund(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":999,"status":"approved","amount":10.00}`
	a := newAdapter(t, s.URL)

	res, err := a.Refund(context.Background(), &core.RefundRequest{
		PaymentID: "100",
		Amount:    core.Money{Amount: 1000, Currency: "PEN"},
		Reason:    core.RefundRequestedByCustomer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.StatusRefunded {
		t.Errorf("expected refunded, got %s", res.Status)
	}
	if res.ID != "999" {
		t.Errorf("expected 999, got %s", res.ID)
	}
	if f.lastReq.Method != http.MethodPost {
		t.Errorf("expected POST for refund, got %s", f.lastReq.Method)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/v1/payments/100/refunds") {
		t.Errorf("expected /v1/payments/100/refunds path, got %s", f.lastReq.URL.Path)
	}
}

func TestTokenize(t *testing.T) {
	s, f := newFakeServer(t)
	f.resp = `{"id":"tok_mp_abc"}`
	a := newAdapter(t, s.URL)

	out, err := a.Tokenize(context.Background(), &core.TokenizeRequest{
		Method: core.MethodCard,
		Card:   &core.CardToken{Token: "tok_front", Last4: "4242", Brand: "visa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderToken != "tok_mp_abc" {
		t.Errorf("expected tok_mp_abc, got %s", out.ProviderToken)
	}
	if out.Last4 != "4242" {
		t.Errorf("expected last4 4242, got %s", out.Last4)
	}
	if !strings.Contains(f.lastReq.URL.Path, "/v1/card_tokens") {
		t.Errorf("expected /v1/card_tokens path, got %s", f.lastReq.URL.Path)
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
	f.resp = `{"message":"cc_rejected_insufficient_funds","status":400,"cause":[{"code":31,"description":"cc_rejected_insufficient_funds"}]}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
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
	if ne.DeclineCode != "cc_rejected_insufficient_funds" {
		t.Errorf("expected decline_code cc_rejected_insufficient_funds, got %s", ne.DeclineCode)
	}
}

func TestRateLimitRetryable(t *testing.T) {
	s, f := newFakeServer(t)
	f.status = 429
	f.resp = `{"message":"Too many requests","status":429}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
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
	f.resp = `{"message":"Service unavailable","status":503}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
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
	f.resp = `{"message":"Invalid access_token","status":401}`
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
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
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.Close()
	a := newAdapter(t, s.URL)

	_, err := a.Charge(context.Background(), &core.ChargeRequest{
		Reference:     "order_x",
		Amount:        core.Money{Amount: 1999, Currency: "PEN"},
		Method:        core.MethodCard,
		ProviderToken: "tok_x",
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
	secret := "whsec_mp_test"
	dataID := "123456789"
	ts := time.Now().Unix()
	manifest := fmt.Sprintf("ts:%d:data.id:%s", ts, dataID)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(manifest))
	sig := hex.EncodeToString(mac.Sum(nil))

	body := fmt.Sprintf(`{"action":"payment.updated","data":{"id":"%s"},"id":100,"date_created":"2026-01-02T15:04:05.000-03:00"}`, dataID)
	req, _ := http.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("X-Signature", fmt.Sprintf("ts=%d,v1=%s", ts, sig))
	req.Header.Set("X-MP-Data-ID", dataID)

	v := &mpVerifier{}
	err := v.Verify([]byte(body), req.Header, secret)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWebhookVerifyBadSignature(t *testing.T) {
	secret := "whsec_mp_test"
	body := `{"action":"payment.updated","data":{"id":"123"}}`
	ts := time.Now().Unix()

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("X-Signature", fmt.Sprintf("ts=%d,v1=deadbeef", ts))
	req.Header.Set("X-MP-Data-ID", "123")

	v := &mpVerifier{}
	if err := v.Verify([]byte(body), req.Header, secret); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestWebhookVerifyStaleTimestamp(t *testing.T) {
	secret := "whsec_mp_test"
	body := `{"action":"payment.updated","data":{"id":"123"}}`
	ts := time.Now().Add(-10 * time.Minute).Unix()
	manifest := fmt.Sprintf("ts:%d:data.id:%s", ts, "123")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(manifest))
	sig := hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("X-Signature", fmt.Sprintf("ts=%d,v1=%s", ts, sig))
	req.Header.Set("X-MP-Data-ID", "123")

	v := &mpVerifier{}
	if err := v.Verify([]byte(body), req.Header, secret); err == nil {
		t.Fatal("expected stale timestamp error")
	}
}

func TestWebhookNormalize(t *testing.T) {
	body := `{"action":"payment.updated","data":{"id":"123456789"},"id":100,"date_created":"2026-01-02T15:04:05.000-03:00","type":"payment","user_id":999}`

	n := &mpNormalizer{}
	ev, err := n.Normalize([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != string(webhook.EventPaymentPending) {
		t.Errorf("expected payment.pending, got %s", ev.Type)
	}
	if ev.Provider != Provider {
		t.Errorf("expected mercadopago provider, got %s", ev.Provider)
	}
}

func TestWebhookNormalizeApprovedAction(t *testing.T) {
	body := `{"action":"payment.approved","data":{"id":"123"},"id":101,"date_created":"2026-01-02T15:04:05.000-03:00"}`

	n := &mpNormalizer{}
	ev, err := n.Normalize([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != string(webhook.EventPaymentCaptured) {
		t.Errorf("expected payment.captured, got %s", ev.Type)
	}
}

// --- Test de registro en factory ---

func TestMPRegisteredInFactory(t *testing.T) {
	caps, ok := factory.Default.Capabilities(Provider)
	if !ok {
		t.Fatal("mercadopago not registered in factory.Default")
	}
	if !caps.SupportsAuthOnly {
		t.Error("mercadopago should support auth-only")
	}
	if !caps.SupportsRefundPartial {
		t.Error("mercadopago should support partial refund")
	}
	// Verifica que PE está en los países soportados.
	found := false
	for _, c := range caps.Countries {
		if c == "PE" {
			found = true
			break
		}
	}
	if !found {
		t.Error("mercadopago should support PE")
	}
}

// --- Test de mappers ---

func TestMapPaymentStatus(t *testing.T) {
	cases := map[string]core.PaymentStatus{
		"approved":            core.StatusCaptured,
		"authorized":          core.StatusAuthorized,
		"in_process":          core.StatusPending,
		"pending":             core.StatusPending,
		"rejected":            core.StatusFailed,
		"cancelled":           core.StatusVoided,
		"refunded":            core.StatusRefunded,
		"partially_refunded":  core.StatusPartiallyRefunded,
		"unknown_weird":       core.StatusFailed,
	}
	for in, want := range cases {
		if got := mapPaymentStatus(in); got != want {
			t.Errorf("mapPaymentStatus(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMapMPAction(t *testing.T) {
	cases := map[string]string{
		"payment.created":         string(webhook.EventPaymentPending),
		"payment.updated":         string(webhook.EventPaymentPending),
		"payment.approved":        string(webhook.EventPaymentCaptured),
		"payment.rejected":        string(webhook.EventPaymentFailed),
		"payment.cancelled":       string(webhook.EventPaymentVoided),
		"payment.refunded":        string(webhook.EventRefundCompleted),
		"payment.partial_refunded": string(webhook.EventRefundCompleted),
		"payment.authorized":      string(webhook.EventPaymentAuthorized),
		"dispute.created":         string(webhook.EventDisputeOpened),
		"dispute.closed":          string(webhook.EventDisputeResolved),
	}
	for in, want := range cases {
		got := mapMPAction(in)
		if got != want {
			t.Errorf("mapMPAction(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMapMPDecline(t *testing.T) {
	cases := map[string]core.ErrorCode{
		"cc_rejected_insufficient_funds":      core.ErrInsufficientFunds,
		"cc_rejected_card_declined":           core.ErrCardDeclined,
		"cc_expired_card":                     core.ErrExpiredCard,
		"cc_rejected_bad_filled_security_code": core.ErrInvalidCVC,
		"cc_rejected_bad_filled_card_number":  core.ErrInvalidNumber,
		"cc_rejected_fraud":                   core.ErrSuspectedFraud,
		"cc_rejected_call_for_authorize":      core.ErrDoNotHonor,
		"cc_rejected_card_error":              core.ErrProcessingError,
	}
	for in, want := range cases {
		ne := mapMPDecline(in, "msg")
		if ne.Code != want {
			t.Errorf("mapMPDecline(%q) code = %s, want %s", in, ne.Code, want)
		}
		if ne.Retryable {
			t.Errorf("mapMPDecline(%q) should not be retryable", in)
		}
	}
}
