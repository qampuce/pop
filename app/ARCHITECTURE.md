# Arquitectura de pop — Payment Orchestration Platform

## Visión General

pop es un SDK de orquestación de pasarelas de pago diseñado para SaaS multi-tenant y multi-país. Su arquitectura sigue principios de **Domain-Driven Design (DDD)** y **Clean Architecture**, con capas bien separadas y contratos explícitos.

```
┌─────────────────────────────────────────────────────────────────┐
│                         HTTP Server Layer                         │
│  (cmd/server + internal/api) — Expone SDK como API REST         │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                      Public SDK Layer                            │
│  (pkg/pop) — API pública que el negocio del tenant importa      │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                    Orchestration Layer                           │
│  (internal/routing + internal/cascading) — Enrutamiento inteligente│
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                      Core Domain Layer                           │
│  (internal/core) — Contratos centrales: Gateway, DTOs, Errors   │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                     Adapter Layer                                │
│  (internal/adapters) — Implementaciones por proveedor          │
└─────────────────────────────────────────────────────────────────┘
```

## Principios de Diseño

### 1. Strategy Pattern para Proveedores
Cada pasarela de pago es una implementación diferente de la interfaz `Gateway`. El Core no conoce detalles de ningún proveedor.

```go
type Gateway interface {
    Provider() ProviderID
    Capabilities() Capabilities
    Tokenize(ctx, *TokenizeRequest) (*TokenizeResponse, error)
    Authorize(ctx, *AuthorizeRequest) (*PaymentResult, error)
    Capture(ctx, *CaptureRequest) (*PaymentResult, error)
    Charge(ctx, *ChargeRequest) (*PaymentResult, error)
    Refund(ctx, *RefundRequest) (*RefundResult, error)
    Void(ctx, *VoidRequest) (*PaymentResult, error)
}
```

### 2. Adapter Pattern para Normalización
Cada adapter traduce el esquema heterogéneo del proveedor a los DTOs canónicos del Core.

- **Entrada**: DTOs canónicos (`ChargeRequest`, `AuthorizeRequest`, etc.)
- **Salida**: DTOs canónicos (`PaymentResult`, `RefundResult`)
- **Errores**: `NormalizedError` con códigos estandarizados

### 3. Factory Pattern para Construcción
El `factory.Registry` construye instancias de adapters a partir de credenciales del tenant.

```go
reg := factory.NewRegistry()
reg.Register(core.ProviderStripe, stripe.Caps, stripe.New)

gw, err := reg.Build(&core.TenantContext{
    TenantID: "tnt_123",
    Provider: core.ProviderStripe,
    Secret:   "sk_live_...",
})
```

### 4. Chain of Responsibility para Cascading
El `cascading.Cascader` implementa reintentos cross-provider con política configurable.

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
```

## Componentes Principales

### Core Domain (`internal/core`)

**Responsabilidad**: Definir el contrato central del dominio de pagos.

**Componentes**:
- `gateway.go`: Interfaz `Gateway` que todos los adapters deben implementar
- `dto.go`: DTOs canónicos (Money, CardToken, Buyer, Request/Result)
- `errors.go`: Jerarquía de errores normalizados (NormalizedError, ErrorCode, ErrorCategory)
- `context.go`: TenantContext + CredentialResolver + CredentialVault (AES-256-GCM)

**Invariantes**:
- El Core nunca importa paquetes de adapters (dependencia unidireccional)
- Todos los montos monetarios están en la unidad mínima (cents) para evitar pérdida de precisión
- Los errores siempre incluyen categoría (validation, auth, decline, fraud, network, etc.)

### Adapter Layer (`internal/adapters`)

**Responsabilidad**: Implementar la interfaz Gateway para cada proveedor.

**Estructura de un adapter**:
```
internal/adapters/{provider}/
├── {provider}.go          — Implementación Gateway
├── {provider}_test.go     — Tests unitarios
├── init.go                — Registro automático en factory.Default
└── webhook.go             — Verificador + normalizador de webhooks
```

**Responsabilidades de un adapter**:
1. Traducir DTOs canónicos al esquema del proveedor
2. Mapear códigos de error del proveedor a `NormalizedError`
3. Implementar verificación de firma de webhooks
4. Normalizar payloads de webhooks a `Event` canónico
5. Manejar idempotencia (idempotency-key)

**Adapter de referencia**: `internal/adapters/mock/` — Implementación completa que sirve como plantilla para adapters reales.

### Factory Layer (`internal/factory`)

**Responsabilidad**: Construir instancias de adapters a partir de credenciales.

**Componentes**:
- `registry.go`: Registro de providers + construcción con credenciales

**Flujo**:
1. Cada adapter se registra en `factory.Default` via `init()`
2. El SDK resuelve credenciales del tenant via `CredentialResolver`
3. El factory construye el adapter con las credenciales resueltas

### Routing Layer (`internal/routing`)

**Responsabilidad**: Seleccionar el mejor proveedor para una request.

**Componentes**:
- `router.go`: Router inteligente por país/moneda/método

**Estrategia de selección**:
1. Filtrar providers por capabilities (country, currency, method)
2. Aplicar blacklist del tenant
3. Ordenar por prioridades (país > método)
4. Devolver lista ordenada para cascading

**RoutingRules del tenant**:
```go
type RoutingRules struct {
    Priorities        map[string][]ProviderID  // Por país
    MethodPriorities  map[PaymentMethod][]ProviderID
    Blacklist         []ProviderID
    Fallbacks         map[ProviderID][]ProviderID
}
```

### Cascading Layer (`internal/cascading`)

**Responsabilidad**: Implementar reintentos cross-provider con política configurable.

**Componentes**:
- `cascading.go`: Cascader con política de reintentos

**Política de reintentos**:
- `MaxAttempts`: Máximo de intentos (default 3)
- `Backoff`: Estrategia de backoff (exponential, linear, fixed)
- `RetryableErrors`: Mapa de códigos de error retryables
- `RetryableCategories`: Categorías de error retryables (network, gateway)

**Flujo de cascading**:
1. Intentar con el primer provider de la lista del router
2. Si falla con error retryable, pasar al siguiente
3. Si todos fallan o error no retryable, devolver error
4. Registrar cada intento en el store para auditoría

### Vault Layer (`internal/vault`)

**Responsabilidad**: Bóveda de tokens de proveedor reutilizables.

**Componentes**:
- `vault.go`: Interfaz Vault + implementación en memoria

**Operaciones**:
- `Store(token, tenantID, providerID)` — Guardar token
- `Get(tokenID)` — Recuperar token
- `List(tenantID, providerID)` — Listar tokens
- `Delete(tokenID)` — Eliminar token

**Uso**: Tokens generados via `Tokenize` pueden guardarse para cargos recurrentes sin que el frontend vuelva a enviar datos sensibles.

### Webhook Layer (`internal/webhook`)

**Responsabilidad**: Normalizar webhooks de todos los proveedores a un formato canónico.

**Componentes**:
- `normalizer.go`: Registry de handlers + procesamiento unificado

**Flujo de procesamiento**:
1. Recibir request HTTP (provider detectado por path)
2. Extraer tenant_id (header > query param > payload)
3. Resolver credenciales del tenant
4. Verificar firma con webhook secret del tenant
5. Normalizar payload a `Event` canónico
6. Devolver `Event` al caller para despacho al bus de eventos

**Eventos canónicos**:
- `payment.authorized`, `payment.captured`, `payment.failed`, `payment.voided`, `payment.pending`
- `refund.created`, `refund.completed`, `refund.failed`
- `dispute.opened`, `dispute.resolved`

### Store Layer (`internal/store`)

**Responsabilidad**: Repositorio in-memory de operaciones para consultas rápidas.

**Componentes**:
- `store.go`: Store thread-safe con índices eficientes

**Índices**:
- `byTenant`: tenantID -> set(paymentID)
- `byProvider`: provider -> set(paymentID)
- `byStatus`: status -> set(paymentID)

**Operaciones**:
- `RecordPayment(op, result)` — Guardar/actualizar payment
- `RecordRefund(result)` — Guardar refund
- `GetPayment(id)` — Recuperar por ID
- `ListPayments(filter)` — Listar con filtros (tenant, status, provider, reference)
- `GetRefund(id)` — Recuperar refund por ID
- `ListRefunds(filter)` — Listar refunds con filtros

**Nota**: No es fuente de verdad distribuida. Cada réplica tiene su propia vista. La persistencia definitiva vive en el proveedor.

### API Layer (`internal/api`)

**Responsabilidad**: Exponer el SDK como API REST.

**Componentes**:
- `server.go`: HTTP server con handlers para todos los endpoints

**Endpoints**:
- `GET /health` — Health check con versión y uptime
- `GET /providers` — Lista providers registrados
- `POST /api/v1/tokenize` — Tokenizar datos de pago
- `POST /api/v1/charge` — Auth+capture con routing+cascading
- `POST /api/v1/authorize` — Auth-only con routing+cascading
- `POST /api/v1/capture` — Capturar autorización previa
- `POST /api/v1/refund` — Reembolsar pago
- `POST /api/v1/void` — Cancelar autorización
- `GET /api/v1/payments` — Listar pagos con filtros
- `GET /api/v1/payments/{id}` — Obtener pago específico
- `GET /api/v1/refunds` — Listar refunds con filtros
- `GET /api/v1/refunds/{id}` — Obtener refund específico
- `GET /api/v1/metrics` — Métricas agregadas (JSON)
- `GET /metrics` — Métricas en formato Prometheus
- `POST /webhooks/{provider}` — Recibir webhook normalizado

**Mapeo de errores a HTTP status**:
- 400: Validation error
- 401: Unauthorized (invalid credentials)
- 402: Payment required (decline)
- 404: Not found (missing credentials)
- 422: Unprocessable entity (unsupported operation)
- 429: Too many requests (rate limited)
- 502: Bad gateway (upstream transient error)
- 500: Internal error

### Public SDK Layer (`pkg/pop`)

**Responsabilidad**: API pública que el negocio del tenant importa.

**Componentes**:
- `client.go`: Client principal + re-exports de tipos

**Re-exports**:
- Todos los tipos del core (Money, CardToken, Buyer, etc.)
- Todas las constantes (Environment, PaymentMethod, ProviderID, etc.)
- Request/Response types extendidos con contexto de tenant

**Uso canónico**:
```go
client, _ := pop.New(pop.Config{
    Credentials: myVault,
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
        ProviderToken: "tok_xxx",
        Capture:       true,
    },
    TenantID: "tnt_123",
    Mode:     pop.Live,
    Country:  "PE",
})
```

## Flujos Principales

### Flujo de Charge (Auth+Capture)

```
1. Tenant llama client.Charge(req)
   ↓
2. Router.Route() selecciona providers aptos
   - Filtra por capabilities (country, currency, method)
   - Aplica blacklist
   - Ordena por prioridades
   ↓
3. Cascading.Run() itera providers en orden
   ↓
4. Para cada provider:
   a. Factory construye adapter con credenciales del tenant
   b. Adapter.Charge() traduce request → esquema proveedor
   c. Adapter llama API del proveedor
   d. Adapter traduce respuesta → PaymentResult canónico
   e. Si error retryable → siguiente provider
   f. Si éxito → devolver PaymentResult
   ↓
5. Server.RecordPayment() guarda en store
   ↓
6. Devolver PaymentResult al tenant
```

### Flujo de Webhook

```
1. Proveedor envía webhook a POST /webhooks/{provider}
   ↓
2. Server.handleWebhook() extrae provider del path
   ↓
3. WebhookRegistry.Process():
   a. Extraer tenant_id (header > query > payload)
   b. Resolver TenantContext con CredentialResolver
   c. Verificar firma con WebhookSecret del tenant
   d. Normalizar payload a Event canónico
   ↓
4. Devolver Event al tenant
   ↓
5. Tenant despacha Event a su bus de eventos
```

## Seguridad

### PCI-DSS Compliance

**Principio**: El backend NUNCA toca PAN/CVV. Solo trabaja con tokens.

**Flujo de tokenización**:
1. Frontend colecta PAN/CVV en formulario seguro
2. Frontend llama API de tokenización del proveedor directamente
3. Proveedor devuelve token opaco (ej. `tok_xxx`)
4. Frontend envía token al backend (pop SDK)
5. Backend usa token para autorizar/capturar

**Vaulting**:
- Tokens pueden guardarse en `internal/vault` para reutilización
- Credenciales de tenant encriptadas en reposo con AES-256-GCM
- Master key derivada de KMS en prod, string fijo en dev

### Webhook Security

**Verificación de firma**:
- Cada adapter implementa su propio `Verifier`
- Stripe: HMAC-SHA256 con header `Stripe-Signature`
- Mercado Pago: HMAC-SHA256 con header `x-signature`
- Kushki: HMAC-SHA256 con header `X-Kushki-Signature`
- dLocal: HMAC-SHA256 con header `X-Signature`
- Niubiz: HMAC-SHA256 con header `X-Signature`
- Adyen: HMAC-SHA256 con header `X-Adyen-Signature` (payload canónico)

**Tenant isolation**:
- Cada tenant tiene su propio webhook secret
- El tenant_id se resuelve antes de verificar la firma
- Un webhook mal firmado para un tenant no afecta a otros

## Performance

### Índices en Store

El store usa índices para consultas eficientes:
- `byTenant`: Para queries por tenant (más común)
- `byProvider`: Para queries por provider
- `byStatus`: Para queries por status

**Estrategia de selección**:
1. Si filtro por provider → usar índice `byProvider`
2. Si filtro por status → usar índice `byStatus`
3. Si filtro por tenant → usar índice `byTenant`
4. Si no hay filtros → full scan (cap 500 resultados)

### Connection Pooling

Cada adapter maneja su propio HTTP client con pool configurable:
```go
var httpClient = &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}
```

### Idempotencia

Todas las operaciones soportan idempotencia via `IdempotencyKey`:
- Generado por el SDK: `{tenant_prefix}:{operation}:{reference}`
- Propagado al proveedor como header `Idempotency-Key`
- El proveedor garantiza que reintentos con la misma key son idempotentes

## Extensibilidad

### Agregar un Nuevo Provider

1. Crear directorio `internal/adapters/{provider}/`
2. Implementar interfaz `Gateway` en `{provider}.go`
3. Implementar `Verifier` y `Normalizer` en `webhook.go`
4. Crear `init.go` para registro en `factory.Default`
5. Escribir tests en `{provider}_test.go`
6. Agregar blank import en `cmd/server/main.go`
7. Actualizar README y CHANGELOG

### Agregar un Nuevo Método de Pago

1. Agregar constante a `core.PaymentMethod`
2. Actualizar `Capabilities.Methods` en adapters que lo soportan
3. Agregar tests de routing para el nuevo método
4. Actualizar documentación

### Agregar una Nueva Operación

1. Agregar método a interfaz `Gateway`
2. Agregar DTOs correspondientes a `core/dto.go`
3. Implementar en todos los adapters (o devolver `ErrUnsupportedOperation`)
4. Agregar handler en `internal/api/server.go`
5. Actualizar `api-manifest.json`
6. Escribir tests

## Testing

### Estrategia de Tests

**Unit tests**: Prueban cada componente en aislamiento
- `internal/core/core_test.go` — Contratos del core
- `internal/factory/registry_test.go` — Factory
- `internal/routing/router_test.go` — Router
- `internal/cascading/cascading_test.go` — Cascading
- `internal/store/store_test.go` — Store

**Integration tests**: Prueban adapters con `httptest`
- `internal/adapters/{provider}/{provider}_test.go` — Cada adapter

**API tests**: Prueban la capa HTTP
- `internal/api/server_test.go` — Endpoints REST

### Coverage Objetivo

- Core: 100% (contratos críticos)
- Adapters: >70% (depende de complejidad del proveedor)
- Orchestration: >80% (routing, cascading)
- API: >80% (handlers)

## Monitorización

### Métricas Expuestas

**JSON** (`GET /api/v1/metrics`):
- Totales de pagos/refunds
- Por status
- Por provider
- Montos procesados
- Por tenant (opcional)

**Prometheus** (`GET /metrics`):
- `pop_uptime_seconds` — Uptime del servicio
- `pop_payments_total` — Total de pagos
- `pop_payments_by_status` — Pagos por status
- `pop_payments_by_provider` — Pagos por provider
- `pop_amount_total` — Monto total procesado
- `pop_refunds_total` — Total de refunds
- `pop_refunds_by_status` — Refunds por status
- `pop_refunds_amount_total` — Monto total reembolsado

### Health Check

**Simple** (`GET /health`):
```json
{
  "status": "ok",
  "service": "pop",
  "version": "0.5.0",
  "uptime_s": 1234
}
```

**Detallado** (`GET /health?detailed=true`):
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
      "providers": ["mock", "stripe", "mercadopago", ...]
    }
  }
}
```

## Deployment

### Docker

**Multi-stage build**:
- Stage `builder`: Go 1.23-alpine para compilar
- Stage `runtime`: Alpine mínimo con el binario

**Health check**:
```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1
```

### Configuración

**Variables de entorno**:
- `PORT`: Puerto del HTTP server (default 8080)
- `POP_MASTER_KEY`: Master key para CredentialVault (32-byte hex)

**En desarrollo**:
- Master key derivada de string fijo (NO usar en prod)
- Tenant `demo` sembrado en memoria contra adapter mock

**En producción**:
- Master key debe venir de KMS
- Cada tenant configura sus credenciales via API de administración
- Webhook secrets rotados periódicamente

## Roadmap

### Corto plazo
- [ ] Agregar más tests de integración end-to-end
- [ ] Implementar persistencia distribuida (Redis/PostgreSQL)
- [ ] Agregar rate limiting por tenant
- [ ] Implementar circuit breaker para providers

### Mediano plazo
- [ ] Soporte para pagos recurrentes (subscriptions)
- [ ] Soporte para split payments (marketplaces)
- [ ] Dashboard de administración web
- [ ] Exportación de métricas a Datadog/New Relic

### Largo plazo
- [ ] Machine learning para routing inteligente
- [ ] Soporte para criptomonedas
- [ ] Multi-region deployment
- [ ] SLA guarantees por tenant
