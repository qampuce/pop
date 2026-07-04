package core

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// TenantContext aísla por tenant toda la configuración sensible y operacional
// que un adapter necesita para invocar a un proveedor.
//
// El SDK se inicializa dinámicamente por petición: NO hay estado global ni
// credenciales compartidas entre tenants. Cada request construye su
// TenantContext (típicamente desde un CredentialResolver que desencripta en
// reposo) y lo pasa al Gateway.
//
// Seguridad:
//   - Secret y WebhookSecret se cargan desencriptados en memoria solo durante
//     la vida de la request. Nunca se loguean ni se serializan a JSON.
//   - El campo Metadata es libre (hasta 4KB) para trazabilidad: order_id,
//     correlation_id, etc. Se propaga al proveedor cuando aplica.
type TenantContext struct {
	// TenantID identifica al merchant (SaaS multi-tenant). Obligatorio.
	TenantID string `json:"tenant_id"`
	// Provider identifica a qué proveedor apunta este contexto.
	Provider ProviderID `json:"provider"`
	// Country en ISO-3166-1 alpha-2 (ej. "PE", "BR", "US"). Obligatorio.
	Country string `json:"country"`
	// Mode del entorno: "test" o "live".
	Mode Environment `json:"mode"`
	// PublicKey del proveedor (puede ser visible en el frontend).
	PublicKey string `json:"public_key,omitempty"`
	// Secret API key del proveedor. NUNCA loguear ni serializar.
	Secret string `json:"-"`
	// WebhookSecret para validar firmas de webhooks del proveedor.
	WebhookSecret string `json:"-"`
	// EndpointURL opcional para override (proxies, sandboxes custom).
	EndpointURL string `json:"endpoint_url,omitempty"`
	// Metadata libre para trazabilidad (correlation_id, order_id...).
	Metadata map[string]string `json:"metadata,omitempty"`
	// IdempotencyPrefix sembrado por el SDK para construir claves
	// idempotentes por operación. Cada adapter lo usa si el proveedor
	// soporta idempotency-key.
	IdempotencyPrefix string `json:"-"`
	// Deadline absoluto heredado del context.Context de la request.
	Deadline time.Time `json:"-"`
}

// Environment del proveedor.
type Environment string

const (
	EnvTest Environment = "test"
	EnvLive Environment = "live"
)

// Validate comprueba campos obligatorios antes de invocar al adapter.
// Es llamado por la factory/router; los adapters pueden asumir un contexto
// válido si llegan acá.
func (t *TenantContext) Validate() error {
	if t == nil {
		return errors.New("pop: TenantContext is nil")
	}
	if t.TenantID == "" {
		return errors.New("pop: tenant_id is required")
	}
	if t.Provider == "" {
		return errors.New("pop: provider is required")
	}
	if t.Country == "" {
		return errors.New("pop: country is required")
	}
	if t.Mode != EnvTest && t.Mode != EnvLive {
		return fmt.Errorf("pop: invalid mode %q (want test|live)", t.Mode)
	}
	if t.Secret == "" {
		return errors.New("pop: secret is required")
	}
	return nil
}

// WithContext devuelve un context.Context derivado de parent con el deadline
// del TenantContext aplicado (si está seteado). Útil para propagar timeouts
// consistentes al HTTP client del adapter.
func (t *TenantContext) WithContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if t.Deadline.IsZero() {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, t.Deadline)
}

// IdempotencyKey construye una clave idempotente determinística por operación.
// Formato: <prefix>:<op>:<ref>. Permite reintentos seguros sin duplicar
// cargos en el proveedor.
func (t *TenantContext) IdempotencyKey(op, ref string) string {
	if ref == "" {
		ref = "auto"
	}
	if t.IdempotencyPrefix == "" {
		t.IdempotencyPrefix = t.TenantID
	}
	return fmt.Sprintf("%s:%s:%s", t.IdempotencyPrefix, op, ref)
}

// CredentialResolver abstrae cómo se obtienen credenciales por tenant.
// La implementación real desencripta en reposo (AES-256-GCM) desde la base
// de datos de configuración del SaaS. El SDK no depende de ningún storage
// concreto: el caller inyecta su resolver.
//
// Esto desacopla el Core del mecanismo de persistencia/encriptación y
// permite mockear credenciales en tests.
type CredentialResolver interface {
	// Resolve devuelve el TenantContext para (tenantID, provider, mode).
	// Si no existe configuración, devuelve ErrCredentialsNotFound.
	Resolve(ctx context.Context, tenantID string, provider ProviderID, mode Environment) (*TenantContext, error)
}

// ErrCredentialsNotFound se devuelve cuando un tenant no tiene configurado
// un provider para el modo solicitado.
var ErrCredentialsNotFound = errors.New("pop: credentials not found for tenant/provider/mode")

// CredentialVault es una implementación de referencia de CredentialResolver
// que desencripta credenciales con AES-256-GCM. El SaaS real puede traer su
// propio resolver; este sirve para tests y para arrancar sin infra.
//
// El secreto maestro (masterKey) debe venir del KMS/secrets manager del
// entorno, NUNCA hardcodeado.
type CredentialVault struct {
	masterKey  []byte                 // 32 bytes para AES-256
	ciphertext map[string][]byte      // key: tenantID|provider|mode -> nonce+ciphertext
}

// NewCredentialVault construye un vault con una master key de 32 bytes.
func NewCredentialVault(masterKey []byte) (*CredentialVault, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("pop: master key must be 32 bytes (got %d)", len(masterKey))
	}
	return &CredentialVault{
		masterKey:  masterKey,
		ciphertext: make(map[string][]byte),
	}, nil
}

// Store encripta y guarda un TenantContext (serializa solo campos seguros).
// El Secret y WebhookSecret se encriptan; el resto viaja en claro dentro
// del payload encriptado (metadata no sensible).
func (v *CredentialVault) Store(tctx *TenantContext) error {
	if err := tctx.Validate(); err != nil {
		return err
	}
	block, err := aes.NewCipher(v.masterKey)
	if err != nil {
		return fmt.Errorf("pop: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("pop: gcm init: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("pop: nonce: %w", err)
	}
	// Serializamos solo lo que necesita reconstruirse. Secret/WebhookSecret
	// viajan dentro del ciphertext (nunca en claro en memoria persistente).
	plain, err := json.Marshal(struct {
		TenantID      string            `json:"tenant_id"`
		Provider      ProviderID        `json:"provider"`
		Country       string            `json:"country"`
		Mode          Environment       `json:"mode"`
		PublicKey     string            `json:"public_key"`
		Secret        string            `json:"secret"`
		WebhookSecret string            `json:"webhook_secret"`
		EndpointURL   string            `json:"endpoint_url"`
		Metadata      map[string]string `json:"metadata"`
	}{
		TenantID:      tctx.TenantID,
		Provider:      tctx.Provider,
		Country:       tctx.Country,
		Mode:          tctx.Mode,
		PublicKey:     tctx.PublicKey,
		Secret:        tctx.Secret,
		WebhookSecret: tctx.WebhookSecret,
		EndpointURL:   tctx.EndpointURL,
		Metadata:      tctx.Metadata,
	})
	if err != nil {
		return fmt.Errorf("pop: marshal: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	v.ciphertext[v.key(tctx)] = ct
	return nil
}

// Resolve implementa CredentialResolver desencriptando bajo demanda.
func (v *CredentialVault) Resolve(ctx context.Context, tenantID string, provider ProviderID, mode Environment) (*TenantContext, error) {
	ct, ok := v.ciphertext[v.keyOf(tenantID, provider, mode)]
	if !ok {
		return nil, ErrCredentialsNotFound
	}
	block, err := aes.NewCipher(v.masterKey)
	if err != nil {
		return nil, fmt.Errorf("pop: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pop: gcm init: %w", err)
	}
	ns := gcm.NonceSize()
	if len(ct) < ns {
		return nil, errors.New("pop: ciphertext too short")
	}
	nonce, body := ct[:ns], ct[ns:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("pop: decrypt: %w", err)
	}
	var raw struct {
		TenantID      string            `json:"tenant_id"`
		Provider      ProviderID        `json:"provider"`
		Country       string            `json:"country"`
		Mode          Environment       `json:"mode"`
		PublicKey     string            `json:"public_key"`
		Secret        string            `json:"secret"`
		WebhookSecret string            `json:"webhook_secret"`
		EndpointURL   string            `json:"endpoint_url"`
		Metadata      map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(plain, &raw); err != nil {
		return nil, fmt.Errorf("pop: unmarshal: %w", err)
	}
	return &TenantContext{
		TenantID:      raw.TenantID,
		Provider:      raw.Provider,
		Country:       raw.Country,
		Mode:          raw.Mode,
		PublicKey:     raw.PublicKey,
		Secret:        raw.Secret,
		WebhookSecret: raw.WebhookSecret,
		EndpointURL:   raw.EndpointURL,
		Metadata:      raw.Metadata,
	}, nil
}

func (v *CredentialVault) key(t *TenantContext) string {
	return v.keyOf(t.TenantID, t.Provider, t.Mode)
}

func (v *CredentialVault) keyOf(tenantID string, p ProviderID, m Environment) string {
	return fmt.Sprintf("%s|%s|%s", tenantID, p, m)
}

// EncodeSecret utilidad para guardar el nonce+ciphertext en base64 (DB).
func EncodeSecret(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeSecret utilidad inversa.
func DecodeSecret(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
