.PHONY: test test-coverage test-integration docker-build up down logs

# ── Unit Tests ───────────────────────────────────────────────────────────────
test:
	go test ./internal/... -count=1 -v

# ── Coverage Report (reporting only, does not fail the build) ─────────────────
# Output: coverage.out (raw profile) + coverage.html (browser-viewable report)
test-coverage:
	go test ./internal/... -count=1 -coverprofile=coverage.out -covermode=atomic
	@echo ""
	@echo "── Coverage by function ──────────────────────────────────────────"
	go tool cover -func=coverage.out
	@echo ""
	go tool cover -html=coverage.out -o coverage.html
	@echo "Report generated: coverage.html"

# ── Integration Tests ─────────────────────────────────────────────────────────
# Spins up MinIO + backend via docker compose, waits until backend is healthy,
# runs integration tests, then tears everything down.
test-integration:
	@echo "▶ Starting services..."
	docker compose up -d --build
	@echo "▶ Waiting for backend to be healthy..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:8080/health > /dev/null 2>&1; then \
			echo "✔ Backend is healthy"; break; \
		fi; \
		echo "  waiting... ($${i}/30)"; \
		sleep 2; \
	done
	@echo "▶ Running integration tests..."
	go test -tags integration -v -count=1 ./test/integration/...
	@echo "▶ Tearing down services..."
	docker compose down

# ── Docker ───────────────────────────────────────────────────────────────────
docker-build:
	docker build -t go-tus-backend:latest .

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f backend
