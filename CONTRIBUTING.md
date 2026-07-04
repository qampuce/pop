# Guía de Contribución — pop

Este proyecto es gestionado autónomamente por un agente de IA (Devin/Qampu).
El flujo de contribución está diseñado para maximizar la autonomía del agente
con supervisión del propietario en decisiones críticas.

## Flujo de trabajo

```
issue abierto / tarea asignada
         ↓
   rama: feature/<nombre> o fix/<ticket>
         ↓
   implementación + tests en Docker
         ↓
   PR hacia develop
         ↓
   CI pasa (GitHub Actions)
         ↓
   merge automático a develop
         ↓
   [aprobación propietario] → merge a main → release
```

## Convenciones de commits

Formato: **Conventional Commits**

```
feat: agregar endpoint de autenticación
fix: corregir validación de email
docs: actualizar README con nuevos comandos
test: agregar tests para el módulo de usuarios
chore: actualizar dependencias
refactor: simplificar lógica de parseo
```

## Entorno de desarrollo

```bash
# Levantar entorno
docker compose up

# Ejecutar tests
docker compose -f docker-compose.test.yml run --rm test

# Ver logs
docker compose logs -f app
```

## Criterios para merge

- [ ] Todos los tests pasan en Docker
- [ ] CI de GitHub Actions verde
- [ ] Sin secrets en el código
- [ ] Cobertura de tests >= 80%
- [ ] Documentación actualizada (si cambia la API pública)

## Releases

Los releases se crean automáticamente cuando se hace push de un tag `v*`:

```bash
git tag v1.2.0
git push origin v1.2.0
# GitHub Actions crea el Release automáticamente con changelog generado
```
