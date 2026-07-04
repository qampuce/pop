# pop — Payment Orchestration Platform

SDK de orquestación de pasarelas de pago para SaaS **multi-tenant** y **multi-país**
(Latam, EE.UU., Europa). Desacopla el core de negocio de las implementaciones
específicas de cada proveedor usando los patrones **Strategy**, **Adapter** y
**Factory**.

## Objetivos de diseño

1. **Máxima flexibilidad**: el Core no conoce a ningún proveedor. Agregar una
   pasarela nueva = registrar un adapter, sin tocar el código de negocio.
2. **Multi-tenancy estricto**: credenciales aisladas por `tenant_id`,
   encriptadas en reposo (AES-256-GCM). El SDK se inicializa por petición.
3. **PCI-DSS**: el backend **nunca** toca PAN/CVV. Trabaja solo con tokens
   generados en el frontend (tokenización + vaulting).
4. **Normalización**: un único contrato de DTOs y eventos para todos los
   proveedores. Errores heterogéneos → `NormalizedError` canónico.

## Estructura del proyecto

```
app/
├── internal/                      ← paquetes privados (no importables externamente)
│   ├── core/                      ← CONTRATO CENTRAL
│   │   ├── gateway.go             ← interfaz Gateway + ProviderID + Capabilities + PaymentMethod
│   │   ├── dto.go                 ← DTOs estándar: Money, CardToken, Buyer, Request/Result
│   │   ├── errors.go              ← NormalizedError + ErrorCode + ErrorCategory
│   │   ├── context.go             ← TenantContext + CredentialResolver + CredentialVault (AES-256-GCM)
│   │   └── core_test.go           ← tests del contrato
│   ├── adapters/                  ← implementaciones por proveedor (Fase 2)
│   │   └── mock/                  ← adapter de referencia (tests + dev local)
│   ├── factory/                   ← registry de adapters: BuildFromCredentials()
│   ├── routing/                   ← router inteligente por país/moneda/método
│   ├── cascading/                 ← reintentos + fallback cross-provider
│   ├── vault/                     ← bóveda de tokens (Store/Get/List/Delete)
│   └── webhook/                   ← normalizador de webhooks unificado
└── pkg/pop/                       ← API PÚBLICA del SDK
    ├── client.go                  ← Client: Charge/Authorize/Capture/Refund/Void/Tokenize/ProcessWebhook
    └── client_test.go             ← tests end-to-end del SDK
```

## Contrato Gateway (Fase 1)

Todo adapter debe implementar estrictamente:

```go
type Gateway interface {
    Provider() core.ProviderID
    Capabilities() core.Capabilities
    Tokenize(ctx, *TokenizeRequest) (*TokenizeResponse, error)
    Authorize(ctx, *AuthorizeRequest) (*PaymentResult, error)
    Capture(ctx, *CaptureRequest) (*PaymentResult, error)
    Charge(ctx, *ChargeRequest) (*PaymentResult, error)
    Refund(ctx, *RefundRequest) (*RefundResult, error)
    Void(ctx, *VoidRequest) (*PaymentResult, error)
}
```

Si un proveedor no soporta una operación, devuelve `ErrUnsupportedOperation`
(nunca paniquea ni silencia).

## Proveedores y métodos abstraídos

- **Proveedores**: Stripe, Mercado Pago, Kushki, dLocal, Niubiz (Perú), Adyen.
- **APMs**: Pix (BR), SPEI (MX), PSE (CO), PagoEfectivo (PE), Yape (PE), Plin (PE).

## Uso canónico

```go
import (
    "github.com/qampu/pop/pkg/pop"
    _ "github.com/qampu/pop/internal/adapters/mock" // o stripe, mercadopago...
)

client, _ := pop.New(pop.Config{
    Credentials: myVault,           // core.CredentialResolver (AES-256-GCM)
    RoutingRules: &pop.RoutingRules{
        Priorities: map[string][]pop.ProviderID{
            "PE": {"niubiz", "mercadopago", "kushki"},
        },
    },
})

res, err := client.Charge(ctx, &pop.ChargeRequestExt{
    ChargeRequest: pop.ChargeRequest{
        Reference:     "order_456",
        Amount:        pop.Money{Amount: 19990, Currency: "PEN"},
        Method:        pop.MethodCard,
        ProviderToken: "tok_xxx",   // del frontend, nunca PAN
        Capture:       true,
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Country:  "PE",
})
```

## Acceso desde celular

🌐 **[https://pop.qampuapp.com](https://pop.qampuapp.com)**

## Desarrollo

```bash
cd app && docker compose up
cd app && docker compose -f docker-compose.test.yml run --rm test
cd app && go test ./...
```

## Estado

- **Fase 1** ✅ Core, interfaces, DTOs, factory, router, cascading, vault,
  webhook normalizer, adapter mock, tests.
- **Fase 2** ⏳ Adaptadores reales: Stripe, Mercado Pago, Kushki, dLocal,
  Niubiz, Adyen.
