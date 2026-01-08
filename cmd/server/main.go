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

	// Just a simple health check on root
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap the uploader handler to support GET for listing
	filesHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Specific check for the listing endpoint
		if r.Method == http.MethodGet && r.URL.Path == "/files/" {
			app.ListFilesHandler(w, r)
			return
		}

		// Fallback to Tus handler for everything else
		app.TusHandler.ServeHTTP(w, r)
	})

	http.Handle("/files/", CORS(filesHandler))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Content-Type, Upload-Defer-Length, Upload-Concat, Location, Upload-Offset, Upload-Length, X-HTTP-Method-Override")
		w.Header().Set("Access-Control-Expose-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Content-Type, Upload-Defer-Length, Upload-Concat, Location, Upload-Offset, Upload-Length")
		
		if r.Method == http.MethodOptions {
			// Preflight requests shouldn't reach the inner handler if it's just for CORS
			// but TUS uses OPTIONS for discovery. We'll set the CORS headers and then
			// let the inner handler settle the TUS part.
		}

		next.ServeHTTP(w, r)
	})
}
