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
		// listing
		if r.Method == http.MethodGet && r.URL.Path == "/files/" {
			app.ListFilesHandler(w, r)
			return
		}

		// Tus protocol
		switch r.Method {
		case http.MethodPost:
			app.TusHandler.PostFile(w, r)
		case http.MethodHead:
			app.TusHandler.HeadFile(w, r)
		case http.MethodPatch:
			app.TusHandler.PatchFile(w, r)
		case http.MethodDelete:
			app.TusHandler.DelFile(w, r)
		case http.MethodGet:
			app.TusHandler.GetFile(w, r)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
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

		// Let the handler handle the request (including OPTIONS)
		next.ServeHTTP(w, r)
	})
}
