package uploader

import "net/http"

// CORS adds cross-origin headers to every response and handles OPTIONS preflight
// by returning 204 No Content immediately — without forwarding to the actual handler.
// Browsers send an OPTIONS preflight before POST/PATCH/DELETE when custom headers
// (e.g. Tus-Resumable) are present; failing to respond with 2xx blocks the real request.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Content-Type, Upload-Defer-Length, Upload-Concat, Location, X-HTTP-Method-Override")
		w.Header().Set("Access-Control-Expose-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Upload-Offset, Location")
		w.Header().Set("Access-Control-Max-Age", "86400")

		// Respond to preflight immediately — do not forward to handlers.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
