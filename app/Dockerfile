# ── Build stage ──────────────────────────────────────────────────────────────
FROM node:22-alpine AS builder

WORKDIR /app

# Instalar dependencias primero (cache layer)
COPY package*.json ./
RUN npm ci --only=production

# Copiar código fuente
COPY src/ ./src/

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM node:22-alpine AS runtime

WORKDIR /app

# Usuario no-root para seguridad
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Instalar nodemon para hot-reload en desarrollo
RUN npm install -g nodemon

COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/src ./src

USER appuser

EXPOSE 3000

# Hot-reload: reinicia automáticamente al cambiar archivos en src/
CMD ["nodemon", "--watch", "src", "--ext", "js,json", "src/index.js"]
