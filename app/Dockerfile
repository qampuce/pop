# ── Runtime stage ────────────────────────────────────────────────────────────
FROM node:22-alpine AS runtime

WORKDIR /app

# Usuario no-root para seguridad.
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Copiar package.json primero para cache de dependencias.
COPY package.json ./

# Instalar dependencias.
RUN npm ci --only=production

# Copiar el resto del código fuente.
COPY . .

# Usuario no-root.
USER appuser

# El server Node.js escucha en 3000 por defecto (override via PORT env).
ENV PORT=3000
EXPOSE 3000

CMD ["node", "src/index.js"]
