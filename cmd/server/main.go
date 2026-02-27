package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"music-streaming/backend/internal/uploader"
)

func main() {
	log.Println("Starting Tus Upload Server...")

	app, err := uploader.NewAppFromEnv()
	if err != nil {
		log.Fatalf("Unable to create app: %v", err)
	}

	// ── Audit producer ───────────────────────────────────────────────────────
	var audit uploader.AuditProducer
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		topic := os.Getenv("KAFKA_TOPIC")
		if topic == "" {
			topic = "codirs-audits"
		}
		audit = uploader.NewKafkaAuditProducer(strings.Split(brokers, ","), topic)
		defer audit.Close() //nolint:errcheck
		log.Printf("Kafka audit producer connected to %s (topic: %s)", brokers, topic)
	} else {
		audit = &uploader.NoopAuditProducer{}
		log.Println("Kafka not configured — audit events discarded (NoopAuditProducer)")
	}
	app.Audit = audit

	// ── Shares client (go-shares) ─────────────────────────────────────────────
	if sharesURL := os.Getenv("GO_SHARES_URL"); sharesURL != "" {
		sharesSecret := os.Getenv("INTERNAL_API_SECRET")
		app.Shares = uploader.NewSharesClient(sharesURL, sharesSecret)
		log.Printf("go-shares client configured (url=%s)", sharesURL)
	} else {
		log.Println("GO_SHARES_URL not set — sharing feature disabled")
	}

	// ── JWT middleware ────────────────────────────────────────────────────────
	issuer := os.Getenv("KEYCLOAK_ISSUER")
	if issuer == "" {
		log.Println("KEYCLOAK_ISSUER not set — JWT validation bypassed (dev mode)")
	}
	wrap := func(h http.Handler) http.Handler {
		return uploader.CORS(uploader.NewJWTMiddleware(issuer, h))
	}

	// ── Health check (no auth required) ──────────────────────────────────────
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK")) //nolint:errcheck
	})

	// ── Routes ────────────────────────────────────────────────────────────────
	//   GET  /files/                    → list files (?bucket=required)
	//   GET  /files/<bucket>/<key>      → download file (proxy)
	//   DELETE /files/<bucket>/<key>    → delete file
	//   POST /files/ (TUS)              → create upload in user bucket
	//   PATCH/HEAD /files/<bucket>/<id> → TUS upload continuation
	http.Handle("/files/", wrap(http.HandlerFunc(app.FilesHandler)))
	http.Handle("/buckets", wrap(http.HandlerFunc(app.BucketsHandler)))
	http.Handle("/buckets/", wrap(http.HandlerFunc(app.BucketItemHandler)))

	// ── Internal routes (server-to-server, shared secret auth) ───────────────
	internalSecret := os.Getenv("INTERNAL_API_SECRET")
	if internalSecret == "" {
		log.Println("INTERNAL_API_SECRET not set — internal endpoints unprotected (dev mode)")
	}
	http.Handle("/internal/provision-user",
		uploader.InternalSecretMiddleware(internalSecret,
			http.HandlerFunc(app.ProvisionUserHandler)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}
}
