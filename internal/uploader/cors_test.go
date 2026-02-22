package uploader

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORS_SetsHeadersOnNormalRequest(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "DELETE")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Tus-Resumable")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Upload-Metadata")
}

func TestCORS_OptionsPreflight_Returns204_DoesNotCallHandler(t *testing.T) {
	handlerCalled := false
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/buckets", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "OPTIONS preflight must return 204")
	assert.False(t, handlerCalled, "underlying handler must NOT be called for OPTIONS preflight")
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORS_OptionsPreflight_FilesEndpoint(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This would return 405 without the preflight fix
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/files/", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Content-Type")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_ExposesUploadHeaders(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPatch, "/files/my-bucket/upload-id", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	exposed := rr.Header().Get("Access-Control-Expose-Headers")
	assert.Contains(t, exposed, "Upload-Offset")
	assert.Contains(t, exposed, "Location")
	assert.Contains(t, exposed, "Tus-Resumable")
}
