// Package store implementa un repositorio in-memory de resultados de
// operaciones de pago (PaymentResult) y reembolsos (RefundResult).
//
// Es la base para los endpoints de consulta (GET /api/v1/payments/{id},
// GET /api/v1/payments) y para trazabilidad/auditoría dentro del proceso.
// La implementación real del SaaS persistirá en su DB; esta interfaz
// permite mockear en tests y arrancar sin infra.
//
// El store es thread-safe y está pensado para ser embebido por el Server.
// No es una fuente de verdad distribuida: cada réplica del orquestador
// tiene su propia vista. La persistencia definitiva vive en el proveedor
// (source of truth) y se accede via los adapters; este store es una
// caché de operaciones recientes para responder queries rápidas sin
// round-trip al proveedor.
package store

import (
	"errors"
	"sync"
	"time"

	"github.com/qampu/pop/internal/core"
)

// ErrNotFound se devuelve cuando no hay registro para el id solicitado.
var ErrNotFound = errors.New("pop: payment not found in store")

// PaymentRecord es un PaymentResult con metadata de auditoría: cuándo se
// almacenó y desde qué operación. Mantiene el DTO de dominio intacto y le
// agrega contexto operacional sin acoplar el core al store.
type PaymentRecord struct {
	*core.PaymentResult
	StoredAt    time.Time `json:"stored_at"`
	Operation   string    `json:"operation"` // charge|authorize|capture|void
	LastUpdated time.Time `json:"last_updated"`
}

// RefundRecord análogo para reembolsos.
type RefundRecord struct {
	*core.RefundResult
	StoredAt time.Time `json:"stored_at"`
}

// Filter para listar payments. Todos los campos son opcionales (zero value
// = no filtrar por ese campo).
type Filter struct {
	TenantID string
	Status   core.PaymentStatus
	Provider core.ProviderID
	Reference string
	// Limit cap de resultados (0 = sin límite, pero se aplica un máximo
	// interno para evitar scans ilimitados).
	Limit int
}

// maxListLimit cap interno para evitar devolver colecciones gigantes.
const maxListLimit = 500

// Store es el repositorio in-memory de operaciones.
type Store struct {
	mu       sync.RWMutex
	payments map[string]*PaymentRecord // key: PaymentResult.ID
	refunds  map[string]*RefundRecord  // key: RefundResult.ID
	// index por tenant para listar sin escanear todo.
	byTenant map[string]map[string]struct{} // tenantID -> set(paymentID)
}

// New construye un Store vacío.
func New() *Store {
	return &Store{
		payments: make(map[string]*PaymentRecord),
		refunds:  make(map[string]*RefundRecord),
		byTenant: make(map[string]map[string]struct{}),
	}
}

// RecordPayment guarda (o actualiza) un PaymentResult junto con la
// operación que lo produjo. Si ya existía un registro con el mismo ID,
// se reemplaza preservando el StoredAt original (auditoría) pero
// actualizando LastUpdated y el resultado.
func (s *Store) RecordPayment(op string, res *core.PaymentResult) {
	if res == nil || res.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := s.payments[res.ID]; ok {
		existing.PaymentResult = res
		existing.Operation = op
		existing.LastUpdated = now
		return
	}
	s.payments[res.ID] = &PaymentRecord{
		PaymentResult: res,
		StoredAt:      now,
		Operation:     op,
		LastUpdated:   now,
	}
	if res.TenantID != "" {
		set, ok := s.byTenant[res.TenantID]
		if !ok {
			set = make(map[string]struct{})
			s.byTenant[res.TenantID] = set
		}
		set[res.ID] = struct{}{}
	}
}

// RecordRefund guarda un RefundResult.
func (s *Store) RecordRefund(res *core.RefundResult) {
	if res == nil || res.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refunds[res.ID] = &RefundRecord{
		RefundResult: res,
		StoredAt:     time.Now().UTC(),
	}
}

// GetPayment recupera un PaymentRecord por ID.
func (s *Store) GetPayment(id string) (*PaymentRecord, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.payments[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Devolvemos una copia del puntero al record (no del PaymentResult
	// interno: el caller no debe mutar el store). Como PaymentRecord
	// contiene *core.PaymentResult, devolvemos el mismo record pero el
	// caller debería tratarlo como read-only.
	return r, nil
}

// ListPayments devuelve los registros que matchean el filtro.
// Orden: por LastUpdated descendente (más reciente primero).
func (s *Store) ListPayments(f Filter) []*PaymentRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = maxListLimit
	}

	var out []*PaymentRecord
	// Si hay filtro por tenant usamos el índice; si no, escaneamos.
	if f.TenantID != "" {
		ids, ok := s.byTenant[f.TenantID]
		if !ok {
			return out
		}
		for id := range ids {
			r := s.payments[id]
			if r == nil {
				continue
			}
			if matchFilter(r, f) {
				out = append(out, r)
			}
		}
	} else {
		for _, r := range s.payments {
			if matchFilter(r, f) {
				out = append(out, r)
			}
		}
	}

	// Ordenar por LastUpdated desc.
	sortByUpdatedDesc(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// CountPayments devuelve la cantidad de registros que matchean el filtro.
func (s *Store) CountPayments(f Filter) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, r := range s.payments {
		if matchFilter(r, f) {
			n++
		}
	}
	return n
}

// matchFilter aplica los campos no-zero del filtro.
func matchFilter(r *PaymentRecord, f Filter) bool {
	if f.TenantID != "" && r.TenantID != f.TenantID {
		return false
	}
	if f.Status != "" && r.Status != f.Status {
		return false
	}
	if f.Provider != "" && r.Provider != f.Provider {
		return false
	}
	if f.Reference != "" && r.Reference != f.Reference {
		return false
	}
	return true
}

// sortByUpdatedDesc ordena in-place por LastUpdated descendente.
// Insertion sort simple: las listas son acotadas (cap maxListLimit).
func sortByUpdatedDesc(rs []*PaymentRecord) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].LastUpdated.Before(rs[j].LastUpdated) {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
