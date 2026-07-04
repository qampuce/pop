// Package vault implementa la bóveda de tokens del SDK: guarda tokens de
// proveedor (NO PAN) asociados a un tenant+buyer para reutilización en
// cargos recurrentes o one-click.
//
// IMPORTANTE: este vault guarda tokens opacos del proveedor, nunca datos
// de tarjeta crudos. El PAN vive (tokenizado) en la bóveda del proveedor
// (Stripe, Kushki...). El SDK solo guarda la referencia.
package vault

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/qampu/pop/internal/core"
)

// StoredToken es un token de proveedor guardado en la bóveda.
type StoredToken struct {
	ID            string            `json:"id"`
	TenantID      string            `json:"tenant_id"`
	BuyerID       string            `json:"buyer_id"`
	Provider      core.ProviderID   `json:"provider"`
	ProviderToken string            `json:"provider_token"`
	Method        core.PaymentMethod `json:"method"`
	Last4         string            `json:"last4,omitempty"`
	Brand         string            `json:"brand,omitempty"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Vault es la abstracción de almacenamiento de tokens. La implementación
// real persiste en la DB del SaaS (encriptada en reposo); esta interfaz
// permite mockear en tests.
type Vault interface {
	Store(ctx context.Context, t *StoredToken) error
	Get(ctx context.Context, tenantID, buyerID, id string) (*StoredToken, error)
	List(ctx context.Context, tenantID, buyerID string) ([]*StoredToken, error)
	Delete(ctx context.Context, tenantID, id string) error
}

// ErrTokenNotFound cuando el token no existe en la bóveda.
var ErrTokenNotFound = errors.New("pop: vault token not found")

// MemoryVault implementación in-memory para tests y arranque sin DB.
type MemoryVault struct {
	mu     sync.RWMutex
	tokens map[string]*StoredToken
}

// NewMemoryVault construye un vault in-memory.
func NewMemoryVault() *MemoryVault {
	return &MemoryVault{tokens: make(map[string]*StoredToken)}
}

func vaultKey(tenantID, id string) string {
	return fmt.Sprintf("%s|%s", tenantID, id)
}

// Store guarda un token.
func (v *MemoryVault) Store(ctx context.Context, t *StoredToken) error {
	if t == nil || t.TenantID == "" || t.ID == "" {
		return errors.New("pop: invalid stored token")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	v.tokens[vaultKey(t.TenantID, t.ID)] = t
	return nil
}

// Get recupera un token por id.
func (v *MemoryVault) Get(ctx context.Context, tenantID, buyerID, id string) (*StoredToken, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	t, ok := v.tokens[vaultKey(tenantID, id)]
	if !ok {
		return nil, ErrTokenNotFound
	}
	if buyerID != "" && t.BuyerID != buyerID {
		return nil, ErrTokenNotFound
	}
	return t, nil
}

// List los tokens de un buyer.
func (v *MemoryVault) List(ctx context.Context, tenantID, buyerID string) ([]*StoredToken, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]*StoredToken, 0)
	for _, t := range v.tokens {
		if t.TenantID == tenantID && (buyerID == "" || t.BuyerID == buyerID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Delete elimina un token.
func (v *MemoryVault) Delete(ctx context.Context, tenantID, id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.tokens, vaultKey(tenantID, id))
	return nil
}
