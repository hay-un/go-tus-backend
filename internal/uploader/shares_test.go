package uploader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSharesApp creates an App with a mock go-shares server.
func newSharesApp(sharesURL string) *App {
	return &App{
		Audit:   &NoopAuditProducer{},
		Content: &NoopContentProducer{},
		Shares:  NewSharesClient(sharesURL, "test-secret"),
	}
}

// ── SharesItemHandler dispatch ────────────────────────────────────────────────

func TestSharesItemHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: nil}
	r := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/shares", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSharesItemHandler_ShouldReturn400_WhenBucketEmpty(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: NewSharesClient("http://unused", "s")}
	r := httptest.NewRequest(http.MethodGet, "/buckets//shares", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"*"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharesItemHandler_ShouldReturn403_WhenNotOwner(t *testing.T) {
	// Arrange — non-owner (no wildcard, bucket not in list)
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: NewSharesClient("http://unused", "s")}
	r := httptest.NewRequest(http.MethodGet, "/buckets/other-bucket/shares", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"user-uuid-files"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestSharesItemHandler_ShouldReturn405_WhenInvalidMethod(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodPut, "/buckets/my-bucket/shares", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// ── GET /buckets/{bucket}/shares ──────────────────────────────────────────────

func TestSharesItemHandler_ShouldReturnShares_WhenListSucceeds(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{
				{"shareeUserId": "rosa@example.com", "permission": "read"},
			},
		})
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/shares", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	data := body["data"].([]interface{})
	assert.Len(t, data, 1)
}

// ── POST /buckets/{bucket}/shares ─────────────────────────────────────────────

func TestSharesItemHandler_ShouldReturn201_WhenCreateShareSucceeds(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "share-1"}) //nolint:errcheck
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	body := `{"shareeEmail":"rosa@example.com","permission":"read"}`
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/shares", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestSharesItemHandler_ShouldReturn400_WhenShareeEmailMissing(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: NewSharesClient("http://unused", "s")}
	body := `{"permission":"read"}`
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/shares", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharesItemHandler_ShouldReturn400_WhenPermissionInvalid(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: NewSharesClient("http://unused", "s")}
	body := `{"shareeEmail":"rosa@example.com","permission":"superadmin"}`
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/shares", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharesItemHandler_ShouldReturn409_WhenShareAlreadyExists(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	body := `{"shareeEmail":"rosa@example.com","permission":"read"}`
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/shares", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestSharesItemHandler_ShouldReturn400_WhenInvalidJSON(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: NewSharesClient("http://unused", "s")}
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/shares", strings.NewReader("{bad json"))
	r.Header.Set("Content-Type", "application/json")
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── DELETE /buckets/{bucket}/shares/{sharee} ──────────────────────────────────

func TestSharesItemHandler_ShouldReturn204_WhenDeleteShare(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket/shares/rosa@example.com", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestSharesItemHandler_ShouldReturn404_WhenDeleteShareNotFound(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket/shares/nobody@example.com", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSharesItemHandler_ShouldReturn500_WhenListSharesFails(t *testing.T) {
	// Arrange — return non-JSON so the decoder fails (sharesclient doesn't check HTTP status)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/shares", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestSharesItemHandler_ShouldReturn500_WhenDeleteShareFails(t *testing.T) {
	// Arrange — go-shares returns 500 for DeleteShare
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	app := newSharesApp(srv.URL)
	r := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket/shares/rosa@example.com", nil)
	r = injectClaims(r, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.SharesItemHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
