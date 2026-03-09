package uploader

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORS_ShouldUseAllowedOriginEnvVar_WhenSet(t *testing.T) {
	// Arrange
	t.Setenv("ALLOWED_ORIGIN", "https://codirs.example.com")
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, "https://codirs.example.com", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_ShouldSetCORSHeaders_WhenNormalRequestReceived(t *testing.T) {
	// Arrange
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "DELETE")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Tus-Resumable")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Upload-Metadata")
}

func TestCORS_ShouldReturn204WithoutCallingHandler_WhenOptionsPreflight(t *testing.T) {
	// Arrange
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

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	require.Equal(t, http.StatusNoContent, rr.Code, "OPTIONS preflight must return 204")
	assert.False(t, handlerCalled, "underlying handler must NOT be called for OPTIONS preflight")
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORS_ShouldReturn204_WhenOptionsPreflightOnFilesEndpoint(t *testing.T) {
	// Arrange — handler simulates what the old broken middleware would do
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/files/", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Content-Type")
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_ShouldExposeUploadHeaders_WhenResponseIsReturned(t *testing.T) {
	// Arrange
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPatch, "/files/my-bucket/upload-id", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	exposed := rr.Header().Get("Access-Control-Expose-Headers")
	assert.Contains(t, exposed, "Upload-Offset")
	assert.Contains(t, exposed, "Location")
	assert.Contains(t, exposed, "Tus-Resumable")
}
