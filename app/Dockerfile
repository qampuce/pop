# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cache de dependencias: copiamos go.mod primero y descargamos.
COPY go.mod ./
RUN go mod download

# Copiamos el resto del código fuente.
COPY . ./

# Build estático del binario del server (CGO disabled para imagen alpine).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/pop-server ./cmd/server

# ── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.20 AS runtime

WORKDIR /app

# Usuario no-root para seguridad.
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# ca-certificates necesario para TLS hacia los proveedores (Stripe, MP, etc.).
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /out/pop-server /app/pop-server

USER appuser

# El server Go escucha en 8080 por defecto (override via PORT env).
ENV PORT=8080
EXPOSE 8080

CMD ["/app/pop-server"]
