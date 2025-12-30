# ==========
# Stage 1: Build
# ==========
FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Linux binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o problab-svr ./cmd/svr

# ==========
# Stage 2: Run
# ==========
FROM gcr.io/distroless/static-debian12
WORKDIR /app

COPY --from=builder /app/problab-svr /app/problab-svr

EXPOSE 5808
ENTRYPOINT ["/app/problab-svr"]