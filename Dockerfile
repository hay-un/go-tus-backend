# Builder Stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy module files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
# CGO_ENABLED=0 for static binary
RUN CGO_ENABLED=0 GOOS=linux go build -o tus-server ./cmd/server

# Final Stage (Distroless-like with Alpine)
FROM alpine:3.19

# Create non-root user
RUN adduser -D -g '' appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/tus-server .

# Use non-root user
USER appuser

EXPOSE 8080

CMD ["./tus-server"]
