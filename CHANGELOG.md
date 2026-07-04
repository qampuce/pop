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
- Cascading: `missing_credentials` ahora es retryable cross-provider para
  que el SDK salte al siguiente provider configurado del tenant en lugar de
  abortar la operación.

## [0.1.0] - 2026-07-04

### Added
- Proyecto inicializado con `new-project.js`
- Docker multi-stage (builder + runtime, usuario no-root)
- Integración continua configurada

[Unreleased]: https://github.com/qampuce/pop/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/qampuce/pop/releases/tag/v0.1.0
