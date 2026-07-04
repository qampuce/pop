# pop


## Acceso desde celular

🌐 **[https://pop.qampuapp.com](https://pop.qampuapp.com)**

URL pública disponible apenas levantes el contenedor.

## Desarrollo rápido

```bash
# Levantar entorno completo (visible en celular automáticamente)
cd app && docker compose up

# Ejecutar tests en contenedor aislado
cd app && docker compose -f docker-compose.test.yml run --rm test

# Ver logs
cd app && docker compose logs -f app
```

## Sin Docker

```bash
cd app
npm install
npm test
npm start
```

## Estructura

```
app/                    → código del proyecto (separado de plantilla)
  src/                  → código fuente
  tests/                → pruebas
  Dockerfile            → imagen de producción (multi-stage)
  docker-compose.yml    → entorno de desarrollo/producción
  docker-compose.test.yml → entorno de tests aislado
logs/                   → logs locales (no en git)
```
