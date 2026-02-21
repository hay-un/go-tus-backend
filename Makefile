.PHONY: test test-integration docker-build up down logs

# ── Unit Tests ───────────────────────────────────────────────────────────────
test:
	go test ./... -v

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
