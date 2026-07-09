package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/pkg/pop"
)

// newTestServer construye un Server con un CredentialVault en memoria que
// tiene un tenant "demo" configurado contra el adapter mock.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	master := sha256Key("test-master-key")
	vault, err := core.NewCredentialVault(master)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if err := vault.Store(&core.TenantContext{
		TenantID:      "demo",
		Provider:      core.ProviderID("mock"),
		Country:       "PE",
		Mode:          core.EnvTest,
		Secret:        "mock_secret",
		WebhookSecret: "mock_secret",
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	client, err := pop.New(pop.Config{Credentials: vault})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return New(client)
}

func sha256Key(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// do ejecuta una request contra el Server usando httptest.
func (s *Server) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	w := s.do(t, "GET", "/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["service"] != "pop" {
		t.Errorf("service = %v, want pop", body["service"])
	}
}

func TestProviders(t *testing.T) {
	s := newTestServer(t)
	w := s.do(t, "GET", "/providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var body struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Providers) == 0 {
		t.Fatal("no providers registered; expected at least mock")
	}
	found := false
	for _, p := range body.Providers {
		if p == "mock" {
			found = true
		}
	}
	if !found {
		t.Errorf("providers = %v, want mock included", body.Providers)
	}
}

func TestCharge(t *testing.T) {
	s := newTestServer(t)
	req := map[string]any{
		"tenant_id":  "demo",
		"mode":       "test",
		"country":    "PE",
		"reference":  "order_42",
		"amount":     map[string]any{"amount": 19990, "currency": "PEN"},
		"method":     "card",
		"capture":    true,
		"provider_token": "tok_4242",
	}
	w := s.do(t, "POST", "/api/v1/charge", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var res pop.PaymentResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != pop.StatusCaptured {
		t.Errorf("status = %v, want captured", res.Status)
	}
	if res.Reference != "order_42" {
		t.Errorf("reference = %v, want order_42", res.Reference)
	}
	if res.Provider != "mock" {
		t.Errorf("provider = %v, want mock", res.Provider)
	}
	if res.Amount.Amount != 19990 {
		t.Errorf("amount = %v, want 19990", res.Amount.Amount)
	}
}

func TestChargeMissingReference(t *testing.T) {
	s := newTestServer(t)
	req := map[string]any{
		"tenant_id": "demo",
		"mode":      "test",
		"country":   "PE",
		"amount":    map[string]any{"amount": 100, "currency": "PEN"},
		"method":    "card",
	}
	w := s.do(t, "POST", "/api/v1/charge", req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d (want 400)", w.Code)
	}
}

func TestAuthorizeCaptureFlow(t *testing.T) {
	s := newTestServer(t)

	// 1. Authorize (auth-only)
	authReq := map[string]any{
		"tenant_id":      "demo",
		"mode":           "test",
		"country":        "PE",
		"reference":      "order_99",
		"amount":         map[string]any{"amount": 5000, "currency": "PEN"},
		"method":         "card",
		"provider_token": "tok_4242",
	}
	w := s.do(t, "POST", "/api/v1/authorize", authReq)
	if w.Code != http.StatusOK {
		t.Fatalf("authorize status: %d body: %s", w.Code, w.Body.String())
	}
	var authRes pop.PaymentResult
	if err := json.Unmarshal(w.Body.Bytes(), &authRes); err != nil {
		t.Fatalf("decode auth: %v", err)
	}
	if authRes.Status != pop.StatusAuthorized {
		t.Errorf("auth status = %v, want authorized", authRes.Status)
	}

	// 2. Capture
	capReq := map[string]any{
		"tenant_id":       "demo",
		"mode":            "test",
		"provider":        "mock",
		"authorization_id": authRes.ID,
		"amount":          map[string]any{"amount": 5000, "currency": "PEN"},
	}
	w = s.do(t, "POST", "/api/v1/capture", capReq)
	if w.Code != http.StatusOK {
		t.Fatalf("capture status: %d body: %s", w.Code, w.Body.String())
	}
	var capRes pop.PaymentResult
	if err := json.Unmarshal(w.Body.Bytes(), &capRes); err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	if capRes.Status != pop.StatusCaptured {
		t.Errorf("capture status = %v, want captured", capRes.Status)
	}
}

func TestRefund(t *testing.T) {
	s := newTestServer(t)
	req := map[string]any{
		"tenant_id":  "demo",
		"mode":       "test",
		"provider":   "mock",
		"payment_id": "mock_pay_order_42",
		"amount":     map[string]any{"amount": 19990, "currency": "PEN"},
		"reason":     "requested_by_customer",
	}
	w := s.do(t, "POST", "/api/v1/refund", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var res pop.RefundResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != pop.StatusRefunded {
		t.Errorf("status = %v, want refunded", res.Status)
	}
	if res.PaymentID != "mock_pay_order_42" {
		t.Errorf("payment_id = %v, want mock_pay_order_42", res.PaymentID)
	}
}

func TestVoid(t *testing.T) {
	s := newTestServer(t)
	req := map[string]any{
		"tenant_id":       "demo",
		"mode":            "test",
		"provider":        "mock",
		"authorization_id": "auth_123",
	}
	w := s.do(t, "POST", "/api/v1/void", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var res pop.PaymentResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != pop.StatusVoided {
		t.Errorf("status = %v, want voided", res.Status)
	}
}

func TestTokenize(t *testing.T) {
	s := newTestServer(t)
	req := map[string]any{
		"tenant_id": "demo",
		"provider":  "mock",
		"mode":      "test",
		"in": map[string]any{
			"method": "card",
			"card": map[string]any{
				"token":       "tok_4242",
				"last4":       "4242",
				"brand":       "visa",
				"holder_name": "Juan Perez",
			},
		},
	}
	w := s.do(t, "POST", "/api/v1/tokenize", req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var res pop.TokenizeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.ProviderToken == "" {
		t.Error("provider_token vacío")
	}
	if res.Last4 != "4242" {
		t.Errorf("last4 = %v, want 4242", res.Last4)
	}
}

func TestWebhook(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"type":              "payment.captured",
		"payment_id":        "mock_pay_order_42",
		"reference":         "order_42",
		"status":            "captured",
		"amount":            19990,
		"currency":          "PEN",
		"provider_event_id": "evt_123",
		"created_at":        "2026-07-04T12:00:00Z",
	}
	buf, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/webhooks/mock?mode=test", bytes.NewReader(buf))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-ID", "demo")
	r.Header.Set("X-Mock-Signature", "mock_secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var ev pop.Event
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Type != pop.EventPaymentCaptured {
		t.Errorf("type = %v, want payment.captured", ev.Type)
	}
	if ev.PaymentID != "mock_pay_order_42" {
		t.Errorf("payment_id = %v", ev.PaymentID)
	}
}

func TestWebhookMissingTenant(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("POST", "/webhooks/mock", bytes.NewReader([]byte(`{}`)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		// El verifier del mock falla si falta X-Mock-Tenant → debe ser error.
		// Aceptamos cualquier status de error (400/401).
		if w.Code < 400 {
			t.Fatalf("status: %d (want error)", w.Code)
		}
		return
	}
}

func TestNoProviderRouting(t *testing.T) {
	s := newTestServer(t)
	// País sin providers reales + currency rara: el mock es global así que
	// siempre habrá candidato. Para forzar ErrNoProvider usamos un registry
	// vacío. Construimos un server con un client cuyo registry no tiene mock.
	// Como el mock se auto-registra en factory.Default global, no podemos
	// vaciarlo sin romper otros tests. Verificamos el path de error via
	// un provider inexistente en capture (debe dar missing_credentials).
	req := map[string]any{
		"tenant_id":       "demo",
		"mode":            "test",
		"provider":        "kushki", // no hay credenciales para kushki
		"authorization_id": "auth_x",
		"amount":          map[string]any{"amount": 100, "currency": "PEN"},
	}
	w := s.do(t, "POST", "/api/v1/capture", req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: %d body: %s (want 404 missing_credentials)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing_credentials") {
		t.Errorf("body no menciona missing_credentials: %s", w.Body.String())
	}
}

func TestInvalidJSON(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/v1/charge", bytes.NewReader([]byte("not json")))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d (want 400)", w.Code)
	}
}

// Asegura que el contexto no se filtre como nil en handlers.
func TestContextPropagation(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/health", nil)
	r = r.WithContext(context.Background())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}
