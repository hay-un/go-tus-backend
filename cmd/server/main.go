package main

import (
	"log"
	"net/http"
	"os"

	"music-streaming/backend/internal/uploader"
)

func main() {
	log.Println("Starting Tus Upload Server...")

	app, err := uploader.NewAppFromEnv()
	if err != nil {
		log.Fatalf("Unable to create app: %v", err)
	}

	// JWT middleware using Spring Boot's JWKS for RS256 validation.
	// When SPRING_AUTH_ISSUER is empty (dev/test), middleware is a no-op.
	springIssuer := os.Getenv("SPRING_AUTH_ISSUER")
	if springIssuer == "" {
		log.Println("SPRING_AUTH_ISSUER not set — JWT auth disabled (dev mode)")
	} else {
		log.Printf("JWT auth enabled — issuer: %s", springIssuer)
	}
	jwtMW := func(h http.Handler) http.Handler {
		return uploader.JWTMiddleware(springIssuer, h)
	}

	// Health check — no auth required
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK")) //nolint:errcheck
	})

	// All /files/ routes — JWT required in production
	//   GET  /files/                    → list files (?bucket=required)
	//   GET  /files/<bucket>/<key>      → download file (proxy)
	//   GET  /files/<bucket>/<key>/stream → streaming (cookie auth for <video src>)
	//   DELETE /files/<bucket>/<key>    → delete file
	//   POST /files/ (TUS)              → create upload in user bucket
	//   PATCH/HEAD /files/<bucket>/<id> → TUS upload continuation
	http.Handle("/files/", uploader.CORS(jwtMW(http.HandlerFunc(app.FilesHandler))))

	// Bucket management routes — JWT required in production
	http.Handle("/buckets", uploader.CORS(jwtMW(http.HandlerFunc(app.BucketsHandler))))
	http.Handle("/buckets/", uploader.CORS(jwtMW(http.HandlerFunc(app.BucketItemHandler))))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}
}
