// Command pop-server arranca el HTTP server del orquestador de pagos.
//
// Expone el SDK pkg/pop como una API REST en el puerto 8080 (override via
// PORT). En modo desarrollo (sin DB) usa un CredentialVault en memoria con
// un tenant "demo" preconfigurado contra el adapter mock, listo para probar
// todos los endpoints sin credenciales reales.
//
//	Uso:
//	  go run ./cmd/server
//	  PORT=8080 go run ./cmd/server
//	  POP_MASTER_KEY=<32-byte hex> go run ./cmd/server
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/qampu/pop/internal/api"
	"github.com/qampu/pop/internal/core"
	"github.com/qampu/pop/pkg/pop"

	// Importar adapters para registro automático en factory.Default
	_ "github.com/qampu/pop/internal/adapters/mock"
	_ "github.com/qampu/pop/internal/adapters/stripe"
	_ "github.com/qampu/pop/internal/adapters/mercadopago"
	_ "github.com/qampu/pop/internal/adapters/kushki"
	_ "github.com/qampu/pop/internal/adapters/dlocal"
	_ "github.com/qampu/pop/internal/adapters/niubiz"
	// _ "github.com/qampu/pop/internal/adapters/adyen" // Temporalmente deshabilitado por problemas de compilación en Windows
)

func main() {
	addr := ":" + envOr("PORT", "8080")

	// Master key para el CredentialVault en memoria. En dev derivamos una
	// determinística de un string fijo; en prod debe venir del KMS.
	masterKey := deriveKey(envOr("POP_MASTER_KEY", "pop-dev-master-key-do-not-use-in-prod"))

	vault, err := core.NewCredentialVault(masterKey)
	if err != nil {
		log.Fatalf("[pop] master key inválida: %v", err)
	}

	// Seed del tenant demo contra el adapter mock en modo test.
	// Permite probar /api/v1/charge etc. sin configurar nada.
	if err := seedDemoTenant(vault); err != nil {
		log.Fatalf("[pop] seed demo tenant: %v", err)
	}

	client, err := pop.New(pop.Config{
		Credentials: vault,
	})
	if err != nil {
		log.Fatalf("[pop] construir client: %v", err)
	}

	srv := api.New(client)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.ListenAndServe(ctx, addr); err != nil {
		log.Printf("[pop] servidor detenido: %v", err)
	}
}

// seedDemoTenant registra un tenant "demo" con el provider mock en modo test
// para que todos los endpoints funcionen out-of-the-box en desarrollo.
func seedDemoTenant(v *core.CredentialVault) error {
	demo := &core.TenantContext{
		TenantID:      "demo",
		Provider:      core.ProviderID("mock"),
		Country:       "PE",
		Mode:          core.EnvTest,
		Secret:        "mock_secret",
		WebhookSecret: "mock_secret",
	}
	if err := v.Store(demo); err != nil {
		return fmt.Errorf("store demo: %w", err)
	}
	log.Printf("[pop] tenant 'demo' sembrado (provider=mock, mode=test, country=PE)")
	return nil
}

func deriveKey(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
