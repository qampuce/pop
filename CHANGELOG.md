# Changelog

Todos los cambios notables de este proyecto están documentados aquí.
Formato: [Keep a Changelog](https://keepachangelog.com/es/1.0.0/)
Versionado: [Semantic Versioning](https://semver.org/lang/es/)

## [Unreleased]

### Added
- Estructura inicial del proyecto con Docker
- Infraestructura de agente autónomo (Devin/Qampu)
- GitHub Actions: CI con tests en Docker
- GitHub Actions: Release automático con tags
- Adapter de Mercado Pago: Payments (auth/capture/charge/void), Refunds,
  Tokenize (card_tokens), webhooks con firma HMAC-SHA256 (`x-signature`),
  mapeo de `cc_rejected_*` a `NormalizedError`, soporte APMs
  (Pix/SPEI/PSE/PagoEfectivo) con NextAction (redirect/QR), idempotency-key.
- Adapter de Kushki: Charges (auth/capture/charge/void), Refunds,
  Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Kushki-Signature`),
  mapeo de códigos de error (K001-K012) a `NormalizedError`, soporte APMs
  (cash/transfer) con NextAction (redirect), idempotency-key, tests completos.
- Cascading: `missing_credentials` ahora es retryable cross-provider para
  que el SDK salte al siguiente provider configurado del tenant en lugar de
  abortar la operación.
- HTTP server (`cmd/server`) que expone el SDK como API REST en puerto 8080:
  `/health`, `/providers`, `/api/v1/{tokenize,charge,authorize,capture,
  refund,void}` y `/webhooks/{provider}`. Mapeo de `NormalizedError` a
  HTTP status codes (402 declines, 502 upstream transitorio, 404 missing
  credentials, etc.). Tenant `demo` sembrado en memoria para dev out-of-the-box.
- Paquete `internal/api` con capa de transporte + tests completos
  (health, providers, charge, authorize+capture flow, refund, void,
  tokenize, webhook, error paths).
- `api-manifest.json` actualizado a v0.2.0 con los 9 endpoints y los
  10 eventos de webhook canónicos.
- Adapter de Adyen: Payments (auth/capture/charge/void), Refunds,
  Tokenize (card tokens), webhooks con firma HMAC-SHA256 (`X-Adyen-Signature`),
  mapeo de códigos de error (101-170) a `NormalizedError` canónico,
  soporte 3DS2 con NextAction (redirect/3DS), idempotency-key,
  tests completos.

### Changed
- Dockerfile corregido para usar Go 1.21 en lugar de Node.js
- Adapter de Adyen habilitado en main.go (ya no está comentado)

## [0.1.0] - 2026-07-04

### Added
- Proyecto inicializado con `new-project.js`
- Docker multi-stage (builder + runtime, usuario no-root)
- Integración continua configurada

[Unreleased]: https://github.com/qampuce/pop/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/qampuce/pop/releases/tag/v0.1.0
