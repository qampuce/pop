// Package api implementa el HTTP server que expone el SDK de orquestación
// de pagos (pkg/pop) como una API REST.
//
// Es la capa de transporte: decodifica JSON de entrada, invoca al Client del
// SDK, y codifica la respuesta (o error normalizado) a JSON. No contiene
// lógica de dominio — toda la orquestación (routing, cascading, vault,
// webhooks) vive en el SDK.
//
// Endpoints:
//   - GET  /health
//   - GET  /providers              → lista providers registrados
//   - POST /api/v1/tokenize        → tokeniza (card/APM)
//   - POST /api/v1/charge          → auth+capture
//   - POST /api/v1/authorize       → auth-only
//   - POST /api/v1/capture         → captura auth previa
//   - POST /api/v1/refund          → reembolso
//   - POST /api/v1/void            → cancela auth pendiente
//   - POST /webhooks/{provider}    → recibe webhook del proveedor
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/pkg/pop"
)

// Server expone el SDK pop.Client sobre HTTP.
type Server struct {
	client *pop.Client
	mux    *http.ServeMux
}

// New construye un Server sobre el Client dado.
func New(c *pop.Client) *Server {
	s := &Server{client: c, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler devuelve el http.Handler listo para servir.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /providers", s.handleProviders)
	s.mux.HandleFunc("POST /api/v1/tokenize", s.handleTokenize)
	s.mux.HandleFunc("POST /api/v1/charge", s.handleCharge)
	s.mux.HandleFunc("POST /api/v1/authorize", s.handleAuthorize)
	s.mux.HandleFunc("POST /api/v1/capture", s.handleCapture)
	s.mux.HandleFunc("POST /api/v1/refund", s.handleRefund)
	s.mux.HandleFunc("POST /api/v1/void", s.handleVoid)
	s.mux.HandleFunc("POST /webhooks/{provider}", s.handleWebhook)
}

// --- Handlers ---------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"service":  "pop",
		"version":  "0.2.0",
		"uptime_s": int64(time.Since(startTime).Seconds()),
	})
}

var startTime = time.Now()

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	// El Client usa factory.Default por defecto (poblado por blank imports de
	// cada adapter en pkg/pop). Listamos los providers registrados ahí.
	ids := factory.Default.Providers()
	out := make([]string, 0, len(ids))
	for _, p := range ids {
		out = append(out, string(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": out,
	})
}

func (s *Server) handleTokenize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID string             `json:"tenant_id"`
		Provider pop.ProviderID     `json:"provider"`
		Mode     pop.Environment    `json:"mode"`
		In       *pop.TokenizeRequest `json:"in"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.In == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "in is required")
		return
	}
	res, err := s.client.Tokenize(r.Context(), req.TenantID, req.Provider, req.Mode, req.In)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCharge(w http.ResponseWriter, r *http.Request) {
	var req pop.ChargeRequestExt
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Capture == false && req.Reference == "" {
		// Capture=false es válido (auth-only), pero reference es obligatorio.
	}
	if req.Reference == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "reference is required")
		return
	}
	res, err := s.client.Charge(r.Context(), &req)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req pop.AuthorizeRequestExt
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Reference == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "reference is required")
		return
	}
	res, err := s.client.Authorize(r.Context(), &req)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var req pop.CaptureRequestExt
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.AuthorizationID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "authorization_id is required")
		return
	}
	res, err := s.client.Capture(r.Context(), &req)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
	var req pop.RefundRequestExt
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.PaymentID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "payment_id is required")
		return
	}
	res, err := s.client.Refund(r.Context(), &req)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleVoid(w http.ResponseWriter, r *http.Request) {
	var req pop.VoidRequestExt
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.AuthorizationID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "authorization_id is required")
		return
	}
	res, err := s.client.Void(r.Context(), &req)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	providerStr := r.PathValue("provider")
	if providerStr == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "provider missing in path")
		return
	}
	// mode se pasa via query ?mode=test|live (default test)
	mode := pop.Environment(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = pop.Test
	}
	ev, err := s.client.ProcessWebhook(r.Context(), pop.ProviderID(providerStr), mode, r)
	if err != nil {
		writeGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// --- Helpers ----------------------------------------------------------------

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[pop-api] encode response: %v", err)
	}
}

// writeGatewayError mapea un error del SDK a una respuesta HTTP apropiada.
// Los NormalizedError del core tienen Code/Category que permiten elegir el
// status code correcto en lugar de devolver siempre 500.
func writeGatewayError(w http.ResponseWriter, err error) {
	var ne *core.NormalizedError
	if errors.As(err, &ne) {
		status := mapNormalizedError(ne)
		writeJSON(w, status, map[string]any{
			"error":            ne.Code,
			"category":         ne.Category,
			"message":          ne.Message,
			"provider":         ne.Provider,
			"provider_code":    ne.ProviderCode,
			"provider_message": ne.ProviderMessage,
			"decline_code":     ne.DeclineCode,
			"retryable":        ne.Retryable,
		})
		return
	}
	// Errores del router (ErrNoProvider) u otros errores planos.
	msg := err.Error()
	if strings.Contains(msg, "no provider available") {
		writeError(w, http.StatusNotFound, "no_provider", msg)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", msg)
}

// mapNormalizedError elige el HTTP status según la categoría/código del error.
func mapNormalizedError(ne *core.NormalizedError) int {
	switch ne.Category {
	case core.CategoryValidation:
		return http.StatusBadRequest
	case core.CategoryAuth:
		if ne.Code == core.ErrMissingCredentials {
			return http.StatusNotFound
		}
		return http.StatusUnauthorized
	case core.CategoryUnsupported:
		return http.StatusUnprocessableEntity
	case core.CategoryDecline, core.CategoryFraud:
		return http.StatusPaymentRequired // 402 — pago rechazado
	case core.CategoryRateLimit:
		return http.StatusTooManyRequests
	case core.CategoryNetwork, core.CategoryGateway:
		if ne.Retryable {
			return http.StatusBadGateway // 502 — fallo transitorio upstream
		}
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error":   code,
		"message": msg,
	})
}

// ListenAndServe arranca el servidor en addr. Bloquea hasta que el servidor
// se detenga. El ctx se usa para shutdown graceful.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("[pop] escuchando en %s", addr)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}
