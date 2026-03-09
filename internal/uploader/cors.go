package uploader

import (
	"net/http"
	"os"
)

// CORS adds cross-origin headers to every response and handles OPTIONS preflight
// by returning 204 No Content immediately — without forwarding to the actual handler.
// Browsers send an OPTIONS preflight before POST/PATCH/DELETE when custom headers
// (e.g. Tus-Resumable) are present; failing to respond with 2xx blocks the real request.
//
// The allowed origin is read from the ALLOWED_ORIGIN env var at middleware creation time.
// Defaults to "*" when not set (dev/test mode).
func CORS(next http.Handler) http.Handler {
	allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
	if allowedOrigin == "" {
		allowedOrigin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Content-Type, Upload-Defer-Length, Upload-Concat, Location, X-HTTP-Method-Override, Range")
		w.Header().Set("Access-Control-Expose-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Location, Accept-Ranges, Content-Range")
		w.Header().Set("Access-Control-Max-Age", "86400")

		// Respond to preflight immediately — do not forward to handlers.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
