# Go TUS Backend

This is the backend service handling resumable uploads for the music streaming platform using the [TUS protocol](https://tus.io/) and MinIO as the storage backend.

## Prerequisites

To build and test the project, ensure you have the following installed:
- [Docker](https://docs.docker.com/get-docker/) & [Docker Compose](https://docs.docker.com/compose/install/)
- [Go](https://go.dev/doc/install) (1.23+)
- `make` (for running Makefile commands)

## 🐳 Building the Docker Image

To build the Docker image locally, you can use the provided Makefile command:

```bash
make docker-build
```
*Alternatively, you can manually run:*
`docker build -t go-tus-backend:latest .`

## 🚀 Running Locally

To spin up the TUS backend and MinIO services locally using Docker Compose:

```bash
make up
```
This will start the TUS backend service on `localhost:8080` and MinIO on `localhost:9000` (Console on `localhost:9001`).

To view container logs:
```bash
make logs
```

To tear down the local environment:
```bash
make down
```

## 🧪 Testing

The project has both **unit tests** (fast, no heavy dependencies) and **integration tests** (require real MinIO infrastructure).

### Running Unit Tests
Unit tests run quickly and do not require external services to be spun up.

```bash
make test
```
*Alternatively, you can manually run:*
`go test ./... -v`

### Running Integration Tests
Integration tests test the end-to-end functionality against a real MinIO container. The Makefile handles spinning up the necessary Docker Compose environment, running the tests, and tearing down the infrastructure safely.

```bash
make test-integration
```
**Under the hood, this will:**
1. Start `minio` and the `backend` containers using `docker compose up -d --build`.
2. Wait for the containers and healthchecks to be ready.
3. Run the Go tests with the `integration` build tag (`go test -tags integration -v ...`).
4. Clean up all running containers once finished via `docker compose down`.

---
*Note: The integration tests use unique prefixes (based on timestamps) for buckets and files so they execute independently without conflicting with each other.*
