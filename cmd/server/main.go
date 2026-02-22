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

	// Health check
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// All /files/ routes handled by the unified FilesHandler:
	//   GET  /files/                    → list files (?bucket=required)
	//   GET  /files/<bucket>/<key>      → download file (proxy)
	//   DELETE /files/<bucket>/<key>    → delete file
	//   POST /files/ (TUS)              → create upload in user bucket
	//   PATCH/HEAD /files/<bucket>/<id> → TUS upload continuation
	http.Handle("/files/", uploader.CORS(http.HandlerFunc(app.FilesHandler)))

	// Bucket management routes
	http.Handle("/buckets", uploader.CORS(http.HandlerFunc(app.BucketsHandler)))
	http.Handle("/buckets/", uploader.CORS(http.HandlerFunc(app.BucketItemHandler)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}
}

