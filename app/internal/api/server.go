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
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/internal/factory"
	"github.com/qampu/pop/internal/store"
	"github.com/qampu/pop/pkg/pop"
)

// Server expone el SDK pop.Client sobre HTTP.
type Server struct {
	client *pop.Client
	store  *store.Store
	mux    *http.ServeMux
}

// New construye un Server sobre el Client dado.
// Si store es nil, se crea uno nuevo in-memory.
func New(c *pop.Client, st *store.Store) *Server {
	if st == nil {
		st = store.New()
	}
	s := &Server{client: c, store: st, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler devuelve el http.Handler listo para servir.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/providers", s.handleProviders)
	s.mux.HandleFunc("/api/v1/tokenize", s.handleTokenize)
	s.mux.HandleFunc("/api/v1/charge", s.handleCharge)
	s.mux.HandleFunc("/api/v1/authorize", s.handleAuthorize)
	s.mux.HandleFunc("/api/v1/capture", s.handleCapture)
	s.mux.HandleFunc("/api/v1/refund", s.handleRefund)
	s.mux.HandleFunc("/api/v1/void", s.handleVoid)
	s.mux.HandleFunc("/api/v1/payments", s.handlePayments)
	s.mux.HandleFunc("/api/v1/payments/", s.handlePayments)
	s.mux.HandleFunc("/api/v1/refunds", s.handleRefunds)
	s.mux.HandleFunc("/api/v1/refunds/", s.handleRefunds)
	s.mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/webhooks/", s.handleWebhook)
}

// --- Handlers ---------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"service":  "pop",
		"version":  "0.5.0",
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	s.store.RecordPayment("charge", res)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	s.store.RecordPayment("authorize", res)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	s.store.RecordPayment("capture", res)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	s.store.RecordPayment("void", res)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Extraer provider del path: /webhooks/{provider}
	// Go 1.21 no tiene PathValue, así que extraemos manualmente
	path := r.URL.Path
	prefix := "/webhooks/"
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid path")
		return
	}
	providerStr := strings.TrimPrefix(path, prefix)
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

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request) {
	// Extraer ID del path: /api/v1/payments/{id}
	path := r.URL.Path
	
	// Normalizar el path para manejar ambos casos: /api/v1/payments y /api/v1/payments/
	prefix := "/api/v1/payments/"
	if !strings.HasPrefix(path, prefix) {
		// Si el path es exactamente /api/v1/payments, agregar trailing slash
		if path == "/api/v1/payments" {
			path = "/api/v1/payments/"
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid path")
			return
		}
	}
	
	id := strings.TrimPrefix(path, prefix)
	
	// Si ID está vacío, es /api/v1/payments/ → listar
	if id == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		
		// Parsear query params
		query := r.URL.Query()
		filter := store.Filter{
			TenantID: query.Get("tenant_id"),
			Status:   core.PaymentStatus(query.Get("status")),
			Provider: core.ProviderID(query.Get("provider")),
			Reference: query.Get("reference"),
		}
		
		// Parsear limit
		if limitStr := query.Get("limit"); limitStr != "" {
			var limit int
			if _, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil {
				filter.Limit = limit
			}
		}
		
		records := s.store.ListPayments(filter)
		writeJSON(w, http.StatusOK, map[string]any{
			"payments": records,
			"count":    len(records),
		})
		return
	}
	
	// GET /api/v1/payments/{id} → obtener un payment específico
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	record, err := s.store.GetPayment(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "payment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleRefunds(w http.ResponseWriter, r *http.Request) {
	// Extraer ID del path: /api/v1/refunds/{id}
	path := r.URL.Path
	
	// Normalizar el path para manejar ambos casos: /api/v1/refunds y /api/v1/refunds/
	prefix := "/api/v1/refunds/"
	if !strings.HasPrefix(path, prefix) {
		// Si el path es exactamente /api/v1/refunds, agregar trailing slash
		if path == "/api/v1/refunds" {
			path = "/api/v1/refunds/"
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid path")
			return
		}
	}
	
	id := strings.TrimPrefix(path, prefix)
	
	// Si ID está vacío, es /api/v1/refunds/ → listar
	if id == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		
		// Parsear query params
		query := r.URL.Query()
		filter := store.Filter{
			TenantID: query.Get("tenant_id"),
			Status:   core.PaymentStatus(query.Get("status")),
			Provider: core.ProviderID(query.Get("provider")),
			Reference: query.Get("payment_id"), // reference en refunds es payment_id
		}
		
		// Parsear limit
		if limitStr := query.Get("limit"); limitStr != "" {
			var limit int
			if _, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil {
				filter.Limit = limit
			}
		}
		
		records := s.store.ListRefunds(filter)
		writeJSON(w, http.StatusOK, map[string]any{
			"refunds": records,
			"count":  len(records),
		})
		return
	}
	
	// GET /api/v1/refunds/{id} → obtener un refund específico
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	record, err := s.store.GetRefund(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "refund not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// Métricas agregadas del store
	allPayments := s.store.ListPayments(store.Filter{})
	allRefunds := s.store.ListRefunds(store.Filter{})
	
	// Calcular estadísticas por status
	statusCounts := make(map[string]int)
	providerCounts := make(map[string]int)
	totalAmount := int64(0)
	
	for _, p := range allPayments {
		statusCounts[string(p.Status)]++
		providerCounts[string(p.Provider)]++
		totalAmount += p.Amount.Amount
	}
	
	// Calcular estadísticas de refunds
	refundCounts := make(map[string]int)
	totalRefunded := int64(0)
	
	for _, r := range allRefunds {
		refundCounts[string(r.Status)]++
		totalRefunded += r.Amount.Amount
	}
	
	writeJSON(w, http.StatusOK, map[string]any{
		"payments": map[string]any{
			"total":         len(allPayments),
			"by_status":    statusCounts,
			"by_provider":  providerCounts,
			"total_amount":  totalAmount,
		},
		"refunds": map[string]any{
			"total":          len(allRefunds),
			"by_status":      refundCounts,
			"total_refunded": totalRefunded,
		},
		"uptime_s": int64(time.Since(startTime).Seconds()),
	})
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
