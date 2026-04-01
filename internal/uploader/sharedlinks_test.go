package uploader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSharedLinksHandler_ShouldReturn401_WhenNoClaims(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[],"total":0,"page":1,"limit":20}`)) //nolint:errcheck
	}))
	defer sharesServer.Close()
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	r := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestListSharedLinksHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Shares: nil}
	r := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"user-1-files"}})
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestListSharedLinksHandler_ShouldReturnPaginatedLinks_WhenSuccessful(t *testing.T) {
	// Arrange
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "shared-links")
		assert.Equal(t, "user-1", r.URL.Query().Get("ownerUserId"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{
				{
					"id":          "link-1",
					"ownerUserId": "user-1",
					"bucket":      "rosa-files",
					"fileKey":     "photo.jpg",
					"expiresAt":   expiresAt.Format(time.RFC3339),
					"createdAt":   time.Now().UTC().Format(time.RFC3339),
				},
			},
			"total": 1,
			"page":  1,
			"limit": 20,
		})
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodGet, "/files/shared-links?page=1&limit=20", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	data := body["data"].([]interface{})
	assert.Len(t, data, 1)
	assert.Equal(t, float64(1), body["total"])
	first := data[0].(map[string]interface{})
	assert.Equal(t, "rosa-files", first["bucket"])
	assert.Equal(t, "photo.jpg", first["fileKey"])
	assert.Equal(t, false, first["expired"])
}

func TestListSharedLinksHandler_ShouldMarkLinkAsExpired_WhenExpiresAtInPast(t *testing.T) {
	// Arrange
	pastExpiresAt := time.Now().UTC().Add(-1 * time.Hour)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{
				{
					"id":          "link-2",
					"ownerUserId": "user-1",
					"bucket":      "rosa-files",
					"fileKey":     "old.mp4",
					"expiresAt":   pastExpiresAt.Format(time.RFC3339),
					"createdAt":   time.Now().UTC().Format(time.RFC3339),
				},
			},
			"total": 1,
			"page":  1,
			"limit": 20,
		})
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	data := body["data"].([]interface{})
	first := data[0].(map[string]interface{})
	assert.Equal(t, true, first["expired"])
	assert.Equal(t, "", first["url"]) // no URL for expired links
}

func TestDeleteSharedLinkHandler_ShouldReturn401_WhenNoClaims(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sharesServer.Close()
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/link-1", nil)
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "link-1")

	// Assert
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestDeleteSharedLinkHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Shares: nil}
	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/link-1", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "link-1")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestDeleteSharedLinkHandler_ShouldReturn204_WhenSuccessful(t *testing.T) {
	// Arrange
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "link-1")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/link-1", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "link-1")

	// Assert
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestDeleteSharedLinkHandler_ShouldReturn404_WhenNotFound(t *testing.T) {
	// Arrange
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"shared link not found"}`)) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/no-such", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "no-such")

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteSharedLinkHandler_ShouldReturn400_WhenIDEmpty(t *testing.T) {
	// Arrange
	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient("http://unused", "test-secret"),
	}
	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "")

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListSharedLinksHandler_ShouldReturn500_WhenListSharedLinksFails(t *testing.T) {
	// Arrange — return non-JSON so the decoder fails (sharesclient doesn't check HTTP status)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDeleteSharedLinkHandler_ShouldReturn500_WhenDeleteFails(t *testing.T) {
	// Arrange — return non-JSON so the decoder fails with a non-"not found" error
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodDelete, "/files/shared-links/link-1", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteSharedLinkHandler(w, r, "link-1")

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestListSharedLinksHandler_ShouldReturn500_WhenSharesReturnsNonOK(t *testing.T) {
	// Arrange — shares server returns non-200 status; covers the status-check branch in ListSharedLinks
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}
	r := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"rosa-files"}})
	w := httptest.NewRecorder()

	// Act
	app.ListSharedLinksHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
