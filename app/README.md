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
│   ├── adapters/                  ← implementaciones por proveedor
│   │   ├── mock/                  ← adapter de referencia (tests + dev local)
│   │   └── stripe/                ← adapter real Stripe (PaymentIntents + webhooks)
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

## Patrones de Uso Avanzados

### Auth-only + Capture Diferido

Para escenarios donde necesitas reservar fondos antes de capturar (ej. envío físico):

```go
// 1. Autorizar (reservar fondos)
auth, err := client.Authorize(ctx, &pop.AuthorizeRequestExt{
    AuthorizeRequest: pop.AuthorizeRequest{
        Reference:     "order_456",
        Amount:        pop.Money{Amount: 19990, Currency: "PEN"},
        Method:        pop.MethodCard,
        ProviderToken: "tok_xxx",
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Country:  "PE",
})

// 2. Capturar después (cuando se confirma el envío)
capture, err := client.Capture(ctx, &pop.CaptureRequestExt{
    CaptureRequest: pop.CaptureRequest{
        AuthorizationID: auth.ID,
        Amount:          pop.Money{Amount: 19990, Currency: "PEN"},
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Provider: auth.Provider,
})
```

### Void de Autorización

Para cancelar una autorización antes de capturar (ej. stock agotado):

```go
void, err := client.Void(ctx, &pop.VoidRequestExt{
    VoidRequest: pop.VoidRequest{
        AuthorizationID: auth.ID,
        Reason:          "out_of_stock",
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Provider: auth.Provider,
})
```

### Reembolso Parcial

Para reembolsos parciales (ej. devolución de un ítem):

```go
refund, err := client.Refund(ctx, &pop.RefundRequestExt{
    RefundRequest: pop.RefundRequest{
        PaymentID: payment.ID,
        Amount:    pop.Money{Amount: 9995, Currency: "PEN"}, // 50% del original
        Reason:    pop.RefundRequestedByCustomer,
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Provider: payment.Provider,
})
```

### Tokenización con Vaulting

Para guardar tokens y reutilizarlos en cargos recurrentes:

```go
// 1. Tokenizar (frontend envía PAN/CVV al proveedor, devuelve token)
tokenize, err := client.Tokenize(ctx, "tnt_123", pop.ProviderStripe, pop.Live, &pop.TokenizeRequest{
    Method: pop.MethodCard,
    Card: &pop.CardToken{
        Token:  "tok_xxx", // del frontend
        Last4:  "4242",
        Brand:  "visa",
    },
})

// 2. Usar token en cargos subsiguientes
charge, err := client.Charge(ctx, &pop.ChargeRequestExt{
    ChargeRequest: pop.ChargeRequest{
        Reference:     "subscription_123",
        Amount:        pop.Money{Amount: 19990, Currency: "USD"},
        Method:        pop.MethodCard,
        ProviderToken: tokenize.ProviderToken, // token reutilizable
        Capture:       true,
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Country:  "US",
})
```

### Routing por Método

Para priorizar providers según el método de pago:

```go
client, _ := pop.New(pop.Config{
    Credentials: myVault,
    RoutingRules: &pop.RoutingRules{
        Priorities: map[string][]pop.ProviderID{
            "PE": {"niubiz", "mercadopago"},
        },
        MethodPriorities: map[pop.PaymentMethod][]pop.ProviderID{
            pop.MethodPix:     {"dlocal", "mercadopago"},
            pop.MethodPSE:     {"kushki", "dlocal"},
            pop.MethodYape:    {"niubiz"},
        },
    },
})
```

### Cascading con Política Personalizada

Para configurar reintentos cross-provider:

```go
policy := cascading.Policy{
    MaxAttempts: 3,
    Backoff:     cascading.ExponentialBackoff,
    RetryableErrors: map[string]bool{
        "network_error": true,
        "timeout": true,
        "missing_credentials": true, // cross-provider
    },
}

client, _ := pop.New(pop.Config{
    Credentials: myVault,
    CascadePolicy: policy,
})
```

### Manejo de Webhooks

Para procesar webhooks normalizados:

```go
// En tu HTTP server:
func handleWebhook(w http.ResponseWriter, r *http.Request) {
    provider := extractProviderFromPath(r.URL.Path) // /webhooks/stripe
    mode := extractModeFromQuery(r.URL.Query())     // ?mode=test|live

    event, err := client.ProcessWebhook(r.Context(), provider, mode, r)
    if err != nil {
        // Error de firma o parseo
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    // Despachar a tu bus de eventos
    switch event.Type {
    case pop.EventPaymentCaptured:
        onPaymentCaptured(event)
    case pop.EventPaymentFailed:
        onPaymentFailed(event)
    case pop.EventRefundCompleted:
        onRefundCompleted(event)
    }
}
```

## Acceso desde celular

🌐 **[https://pop.qampuapp.com](https://pop.qampuapp.com)**

## API REST

El HTTP server expone el SDK como una API REST en el puerto 8080. Todos los endpoints aceptan JSON y devuelven JSON con los tipos canónicos del SDK.

### Endpoints

#### GET /health
Health check del servicio con versión y uptime.

Query params:
- `detailed=true` — incluye estado de componentes (store, factory)

```json
{
  "status": "ok",
  "service": "pop",
  "version": "0.5.0",
  "uptime_s": 1234,
  "components": {
    "store": {
      "status": "ok",
      "payments": 42,
      "refunds": 5
    },
    "factory": {
      "status": "ok",
      "providers": ["mock", "stripe", "mercadopago", "kushki", "dlocal", "niubiz", "adyen"]
    }
  }
}
```

#### GET /providers
Lista los providers de pago registrados en el orquestador.

Query params:
- `detailed=true` — incluye capabilities de cada provider

```json
{
  "providers": ["mock", "stripe", "mercadopago", "kushki", "dlocal", "niubiz", "adyen"]
}
```

Con `detailed=true`:
```json
{
  "providers": {
    "mock": {
      "countries": [],
      "currencies": [],
      "methods": ["card", "pix", "spei", "pse", "pagoefectivo", "yape", "plin"],
      "supports_auth_only": true,
      "supports_refund_partial": true,
      "supports_vaulting": true
    },
    "stripe": {
      "countries": [],
      "currencies": [],
      "methods": ["card"],
      "supports_auth_only": true,
      "supports_refund_partial": true,
      "supports_vaulting": true
    }
  }
}
```

#### POST /api/v1/tokenize
Tokeniza datos de pago (card/APM) via el provider indicado.

```json
{
  "tenant_id": "demo",
  "provider": "mock",
  "mode": "test",
  "in": {
    "method": "card",
    "card": {
      "token": "tok_test_123",
      "last4": "4242",
      "brand": "visa"
    }
  }
}
```

#### POST /api/v1/charge
Ejecuta autorización + captura en una operación con routing y cascading.

```json
{
  "tenant_id": "demo",
  "provider": "mock",
  "mode": "test",
  "country": "PE",
  "reference": "order_456",
  "amount": {
    "amount": 19990,
    "currency": "PEN"
  },
  "method": "card",
  "provider_token": "tok_test_123",
  "capture": true
}
```

#### POST /api/v1/authorize
Reserva fondos sin capturar (auth-only) con routing y cascading.

```json
{
  "tenant_id": "demo",
  "provider": "mock",
  "mode": "test",
  "country": "PE",
  "reference": "order_456",
  "amount": {
    "amount": 19990,
    "currency": "PEN"
  },
  "method": "card",
  "provider_token": "tok_test_123"
}
```

#### POST /api/v1/capture
Captura fondos de una autorización previa contra el provider original.

```json
{
  "tenant_id": "demo",
  "authorization_id": "auth_123",
  "amount": {
    "amount": 19990,
    "currency": "PEN"
  }
}
```

#### POST /api/v1/refund
Devuelve fondos total o parcialmente de un pago capturado.

```json
{
  "tenant_id": "demo",
  "payment_id": "pay_123",
  "amount": {
    "amount": 19990,
    "currency": "PEN"
  },
  "reason": "requested_by_customer"
}
```

#### POST /api/v1/void
Cancela una autorización pendiente antes de su captura.

```json
{
  "tenant_id": "demo",
  "authorization_id": "auth_123",
  "reason": "duplicate"
}
```

#### POST /webhooks/{provider}
Recibe y normaliza webhooks del proveedor indicado (firma verificada).

Query params: `?mode=test|live` (default test)

Headers requeridos según provider:
- Stripe: `Stripe-Signature`
- Mercado Pago: `x-signature`
- Kushki: `X-Kushki-Signature`
- dLocal: `X-Signature`
- Niubiz: `X-Signature`
- Adyen: `X-Adyen-Signature`, `X-Adyen-Webhook-Secret`, `X-Adyen-Tenant`

#### GET /api/v1/refunds
Lista refunds con filtros (tenant_id, status, provider, payment_id, limit).

```json
{
  "refunds": [...],
  "count": 10
}
```

Query params:
- `tenant_id`: filtro por tenant
- `status`: filtro por status (refunded, failed, etc.)
- `provider`: filtro por provider
- `payment_id`: filtro por payment_id original
- `limit`: límite de resultados (default 50, max 500)

#### GET /api/v1/refunds/{id}
Obtiene un refund específico por ID.

#### GET /api/v1/metrics
Métricas agregadas de pagos y refunds (totales, por status, por provider, montos).

Query params:
- `by_tenant=true` — incluye métricas desglosadas por tenant

```json
{
  "payments": {
    "total": 100,
    "by_status": {"captured": 80, "failed": 20},
    "by_provider": {"mock": 60, "stripe": 40},
    "total_amount": 1500000
  },
  "refunds": {
    "total": 5,
    "by_status": {"refunded": 5},
    "total_refunded": 50000
  },
  "uptime_s": 3600,
  "by_tenant": {
    "tenant_a": {
      "payments": 50,
      "amount": 750000,
      "refunds": 3
    },
    "tenant_b": {
      "payments": 50,
      "amount": 750000,
      "refunds": 2
    }
  }
}
```

#### GET /metrics
Métricas en formato Prometheus para integración con sistemas de monitoreo (Grafana, Prometheus, etc.).

Formato: text/plain con métricas tipo gauge para:
- `pop_uptime_seconds` — uptime del servicio
- `pop_payments_total` — total de pagos procesados
- `pop_payments_by_status` — pagos por status (captured, failed, etc.)
- `pop_payments_by_provider` — pagos por provider (mock, stripe, etc.)
- `pop_amount_total` — monto total procesado en cents
- `pop_refunds_total` — total de refunds procesados
- `pop_refunds_by_status` — refunds por status
- `pop_refunds_amount_total` — monto total reembolsado en cents

### Errores

Los errores se devuelven con HTTP status codes apropiados y un JSON con detalles:

```json
{
  "error": "card_declined",
  "category": "decline",
  "message": "Card declined",
  "provider": "stripe",
  "provider_code": "card_declined",
  "provider_message": "Your card was declined.",
  "decline_code": "generic_decline",
  "retryable": false
}
```

HTTP status codes:
- 400: Invalid request (validation error)
- 401: Unauthorized (invalid credentials)
- 402: Payment required (declined)
- 404: Not found (missing credentials)
- 422: Unprocessable entity (unsupported operation)
- 429: Too many requests (rate limited)
- 502: Bad gateway (upstream transient error)
- 500: Internal error

## Desarrollo

```bash
cd app && docker compose up
cd app && docker compose -f docker-compose.test.yml run --rm test
cd app && go test ./...
cd app && go run ./cmd/server
```

## Estado

- **Fase 1** ✅ Core, interfaces, DTOs, factory, router, cascading, vault,
  webhook normalizer, adapter mock, tests.
- **Fase 2** 🚧 Adaptadores reales:
  - ✅ **Stripe** — PaymentIntents (auth/capture/charge/void), Refunds,
    Tokenize (PaymentMethods), webhooks con firma HMAC-SHA256, mapeo de
    decline codes a `NormalizedError` canónico, idempotency-key, tests con
    `httptest` (70.9% cobertura).
  - ✅ **Mercado Pago** — Payments (auth/capture/charge/void), Refunds,
    Tokenize (card_tokens), webhooks con firma HMAC-SHA256 (`x-signature`),
    mapeo de `cc_rejected_*` status_detail a `NormalizedError` canónico,
    soporte APMs (Pix/SPEI/PSE/PagoEfectivo) con NextAction (redirect/QR),
    idempotency-key, tests con `httptest` (69.1% cobertura).
  - ✅ **Kushki** — Charges (auth/capture/charge/void), Refunds,
    Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Kushki-Signature`),
    mapeo de códigos de error (K001-K012) a `NormalizedError` canónico,
    soporte APMs (cash/transfer) con NextAction (redirect), idempotency-key,
    tests con `httptest` (cobertura completa).
  - ✅ **dLocal** — Payments (auth/capture/charge/void), Refunds,
    Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Signature`),
    mapeo de códigos de error (4001-5009) a `NormalizedError` canónico,
    soporte APMs (Pix/SPEI/PSE/PagoEfectivo) con NextAction (redirect/QR),
    idempotency-key, tests con `httptest` (cobertura completa).
  - ✅ **Niubiz** — Payments (auth/capture/charge/void), Refunds,
    Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Signature`),
    mapeo de códigos de error (1001-1009) a `NormalizedError` canónico,
    soporte APMs (Yape/Plin) con NextAction (redirect/deep link/QR),
    idempotency-key, tests con `httptest` (cobertura completa).
  - ✅ **Adyen** — Payments (auth/capture/charge/void), Refunds,
    Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Adyen-Signature`)
    usando payload canónico según especificación oficial (pspReference,
    originalReference, merchantAccountCode, merchantReference, value, currency,
    eventCode, success), mapeo de códigos de error (101-170) a `NormalizedError`
    canónico, soporte 3DS2 con NextAction (redirect/3DS), idempotency-key,
    tests completos (adapter + webhook).
