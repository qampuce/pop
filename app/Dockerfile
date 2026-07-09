FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
COPY internal ./internal
COPY cmd ./cmd
COPY pkg ./pkg
RUN go mod download
RUN go build -o pop-server ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/pop-server .
ENV PORT=8080
EXPOSE 8080
CMD ./pop-server
