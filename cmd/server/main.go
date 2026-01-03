package main

import (
	"log"
	"net/http"
	"os"

	"music-streaming/backend/internal/uploader"
)

func main() {
	log.Println("Starting Tus Upload Server...")

	handler, err := uploader.NewHandlerFromEnv()
	if err != nil {
		log.Fatalf("Unable to create handler: %v", err)
	}

	// Just a simple health check on root
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Mount the uploader handler
	// NewHandler already strips the prefix "/files/", so we handle it at root of the mux or specific path?
	// In NewHandler I did: http.StripPrefix("/files/", tusHandler)
	// So if I mount it at /files/, it matches.
	http.Handle("/files/", handler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}
}
