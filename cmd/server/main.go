package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"

	"codirs/backend/internal/uploader"
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

	// ── Content producer ──────────────────────────────────────────────────────
	var contentProducer uploader.ContentProducer
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		contentTopic := os.Getenv("CONTENT_KAFKA_TOPIC")
		if contentTopic == "" {
			contentTopic = "codirs-content"
		}
		contentProducer = uploader.NewKafkaContentProducer(strings.Split(brokers, ","), contentTopic)
		defer contentProducer.Close() //nolint:errcheck
		log.Printf("Kafka content producer connected to %s (topic: %s)", brokers, contentTopic)
	} else {
		contentProducer = &uploader.NoopContentProducer{}
		log.Println("Kafka not configured — content events discarded (NoopContentProducer)")
	}
	app.Content = contentProducer

	// ── Keycloak granter (sets allowed_buckets attribute after provisioning) ──
	keycloakGranter := uploader.NewHTTPKeycloakGranter(
		os.Getenv("KEYCLOAK_INTERNAL_ISSUER"),
		os.Getenv("KEYCLOAK_ADMIN_USERNAME"),
		os.Getenv("KEYCLOAK_ADMIN_PASSWORD"),
	)
	if keycloakGranter != nil {
		app.KeycloakGranter = keycloakGranter
		log.Println("Keycloak granter configured — bucket provisioning will set allowed_buckets")
	} else {
		log.Println("Keycloak admin credentials not set — bucket provisioning skips Keycloak attribute update")
	}

	// ── Vault client (SSE-KMS key lifecycle) ─────────────────────────────────
	if vaultAddr := os.Getenv("VAULT_ADDR"); vaultAddr != "" {
		vaultToken := os.Getenv("VAULT_TOKEN")
		vc := uploader.NewVaultClient(vaultAddr, vaultToken)
		if vc != nil {
			app.VaultClient = vc
			log.Printf("Vault client configured (addr=%s) — SSE-KMS key lifecycle enabled", vaultAddr)
		} else {
			log.Println("VAULT_TOKEN not set — SSE-KMS key lifecycle disabled")
		}
	} else {
		log.Println("VAULT_ADDR not set — SSE-KMS key lifecycle disabled")
	}

	// ── Shares client (go-shares) ─────────────────────────────────────────────
	if sharesURL := os.Getenv("GO_SHARES_URL"); sharesURL != "" {
		sharesSecret := os.Getenv("INTERNAL_API_SECRET")
		app.Shares = uploader.NewSharesClient(sharesURL, sharesSecret)
		log.Printf("go-shares client configured (url=%s)", sharesURL)
	} else {
		log.Println("GO_SHARES_URL not set — sharing feature disabled")
	}

	// ── Purge consumer (Kafka topic: codirs-purge) ────────────────────────────
	// Listens for bucket.purge events published by go-shares purge ticker.
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		purgeTopic := os.Getenv("KAFKA_PURGE_TOPIC")
		if purgeTopic == "" {
			purgeTopic = "codirs-purge"
		}
		reader := kafka.NewReader(kafka.ReaderConfig{
			Brokers:  strings.Split(brokers, ","),
			Topic:    purgeTopic,
			GroupID:  "go-tus-purge",
			MinBytes: 1,
			MaxBytes: 1e6,
			MaxWait:  1 * time.Second,
		})
		go func() {
			defer reader.Close() //nolint:errcheck
			log.Printf("Purge consumer started (topic: %s)", purgeTopic)
			for {
				msg, err := reader.ReadMessage(context.Background())
				if err != nil {
					log.Printf("purge consumer: read error: %v", err)
					continue
				}
				app.ProcessPurgeEvent(context.Background(), msg.Value)
			}
		}()
	}

	// ── JWT middleware ────────────────────────────────────────────────────────
	issuer := os.Getenv("KEYCLOAK_ISSUER")
	if issuer == "" {
		log.Println("KEYCLOAK_ISSUER not set — JWT validation bypassed (dev mode)")
	}
	// KEYCLOAK_INTERNAL_ISSUER: internal Docker URL for JWKS fetch (avoids custom CA in container).
	// Falls back to KEYCLOAK_ISSUER if not set (e.g. prod with public CA cert).
	internalIssuer := os.Getenv("KEYCLOAK_INTERNAL_ISSUER")
	if internalIssuer == "" {
		internalIssuer = issuer
	}
	jwksURL := internalIssuer + "/protocol/openid-connect/certs"
	wrap := func(h http.Handler) http.Handler {
		return uploader.CORS(uploader.NewJWTMiddleware(issuer, jwksURL, h))
	}

	// ── Health check (no auth required) ──────────────────────────────────────
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK")) //nolint:errcheck
	})

	// ── Routes ────────────────────────────────────────────────────────────────
	//   GET  /files/                    → list files (?bucket=required)
	//   GET  /files/<bucket>/<key>      → download file (proxy)
	//   GET  /files/<bucket>/<key>/stream → streaming (cookie auth for <video src>)
	//   DELETE /files/<bucket>/<key>    → delete file
	//   POST /files/ (TUS)              → create upload in user bucket
	//   PATCH/HEAD /files/<bucket>/<id> → TUS upload continuation
	http.Handle("/files/", wrap(http.HandlerFunc(app.FilesHandler)))
	http.Handle("/buckets", wrap(http.HandlerFunc(app.BucketsHandler)))
	http.Handle("/buckets/", wrap(http.HandlerFunc(app.BucketItemHandler)))
	http.Handle("/users/me", wrap(http.HandlerFunc(app.DeleteAccountHandler)))

	// ── Public share download routes (no auth — gated by per-link password) ──
	http.Handle("/share/", uploader.CORS(http.HandlerFunc(app.ShareDownloadHandler)))

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
