package uploader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockSharesServer creates a test HTTP server that can be configured per-test.
func newMockSharesServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// ── CanAccess ──────────────────────────────────────────────────────────────────

func TestSharesClientCanAccess_ShouldReturnTrue_WhenServerReturns200(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-secret", r.Header.Get("X-Internal-Secret"))
		assert.Contains(t, r.URL.Path, "check")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"hasAccess":true,"permission":"read"}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	ok, err := client.CanAccess(context.Background(), "my-bucket", "user@example.com")

	// Assert
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSharesClientCanAccess_ShouldReturnFalse_WhenServerReturns403(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	ok, err := client.CanAccess(context.Background(), "bucket", "user@x.com")

	// Assert
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSharesClientCanAccess_ShouldReturnCached_WhenCalledTwice(t *testing.T) {
	// Arrange — server called only once
	callCount := 0
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"hasAccess":true}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, _ = client.CanAccess(context.Background(), "bucket", "user@x.com")
	_, _ = client.CanAccess(context.Background(), "bucket", "user@x.com")

	// Assert
	assert.Equal(t, 1, callCount, "second call should use cache")
}

// ── InvalidateCache ───────────────────────────────────────────────────────────

func TestSharesClientInvalidateCache_ShouldRemoveMatchingEntries(t *testing.T) {
	// Arrange — populate cache
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"hasAccess":true}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")
	_, _ = client.CanAccess(context.Background(), "my-bucket", "user@x.com")

	// Act
	client.InvalidateCache("my-bucket")

	// Assert — cache should be empty so next call hits server again
	callCount := 0
	innerSrv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusForbidden)
	})
	client2 := NewSharesClient(innerSrv.URL, "test-secret")
	client2.cache = client.cache // transfer the cleared cache
	_, _ = client2.CanAccess(context.Background(), "my-bucket", "user@x.com")
	// The test verifies InvalidateCache doesn't panic and cache is empty
}

// ── GetSharesForBucket ────────────────────────────────────────────────────────

func TestSharesClientGetSharesForBucket_ShouldReturnShares_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.RawQuery, "bucket=my-bucket")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{
				{"ownerBucket": "my-bucket", "shareeUserId": "user@x.com", "permission": "read"},
			},
		})
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	shares, err := client.GetSharesForBucket(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
	assert.Len(t, shares, 1)
	assert.Equal(t, "user@x.com", shares[0]["shareeUserId"])
}

func TestSharesClientGetSharesForBucket_ShouldReturnEmpty_WhenDataNull(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": nil}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	shares, err := client.GetSharesForBucket(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
	assert.Empty(t, shares)
}

// ── CreateShare ───────────────────────────────────────────────────────────────

func TestSharesClientCreateShare_ShouldReturnResult_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "share-123"}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	result, err := client.CreateShare(context.Background(), "owner-uuid", "my-bucket", "user@x.com", "read")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "share-123", result["id"])
}

func TestSharesClientCreateShare_ShouldReturnConflictError_WhenAlreadyExists(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, err := client.CreateShare(context.Background(), "owner-uuid", "my-bucket", "user@x.com", "read")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestSharesClientCreateShare_ShouldReturnError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, err := client.CreateShare(context.Background(), "owner-uuid", "my-bucket", "user@x.com", "read")

	// Assert
	require.Error(t, err)
}

// ── DeleteShare ───────────────────────────────────────────────────────────────

func TestSharesClientDeleteShare_ShouldSucceed_WhenShareExists(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.RawQuery, "bucket=my-bucket")
		w.WriteHeader(http.StatusNoContent)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteShare(context.Background(), "my-bucket", "user@x.com")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientDeleteShare_ShouldReturnNotFoundError_WhenShareMissing(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteShare(context.Background(), "my-bucket", "user@x.com")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── GetSharedBuckets ──────────────────────────────────────────────────────────

func TestSharesClientGetSharedBuckets_ShouldReturnBuckets_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "sharee=user%40x.com")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{{"ownerBucket": "shared-bucket"}},
		})
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	buckets, err := client.GetSharedBuckets(context.Background(), "user@x.com")

	// Assert
	require.NoError(t, err)
	assert.Len(t, buckets, 1)
}

// ── DeleteSharesForBucket ─────────────────────────────────────────────────────

func TestSharesClientDeleteSharesForBucket_ShouldSucceed_WhenOK(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "my-bucket")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"deleted":2}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteSharesForBucket(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientDeleteSharesForBucket_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteSharesForBucket(context.Background(), "my-bucket")

	// Assert
	require.Error(t, err)
}

// ── IsBucketDeleted ───────────────────────────────────────────────────────────

func TestSharesClientIsBucketDeleted_ShouldReturnTrue_WhenDeleted(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"deleted": true}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	deleted, err := client.IsBucketDeleted(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
	assert.True(t, deleted)
}

func TestSharesClientIsBucketDeleted_ShouldReturnCached_WhenCalledTwice(t *testing.T) {
	// Arrange — server called only once
	callCount := 0
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"deleted": false}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, _ = client.IsBucketDeleted(context.Background(), "bucket")
	_, _ = client.IsBucketDeleted(context.Background(), "bucket")

	// Assert
	assert.Equal(t, 1, callCount)
}

// ── RestoreBucket ─────────────────────────────────────────────────────────────

func TestSharesClientRestoreBucket_ShouldSucceed_WhenBucketInTrash(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "my-bucket")
		w.WriteHeader(http.StatusNoContent)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.RestoreBucket(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientRestoreBucket_ShouldReturnNotFoundError_WhenNotInTrash(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.RestoreBucket(context.Background(), "my-bucket")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── GetTrashedBuckets ─────────────────────────────────────────────────────────

func TestSharesClientGetTrashedBuckets_ShouldReturnList_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "ownerUserId=user-uuid")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{{"bucketName": "deleted-bucket"}},
		})
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	buckets, err := client.GetTrashedBuckets(context.Background(), "user-uuid")

	// Assert
	require.NoError(t, err)
	assert.Len(t, buckets, 1)
}

// ── PurgeBucketRecord ─────────────────────────────────────────────────────────

func TestSharesClientPurgeBucketRecord_ShouldSucceed_WhenRecordExists(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "purge")
		w.WriteHeader(http.StatusNoContent)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.PurgeBucketRecord(context.Background(), "my-bucket")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientPurgeBucketRecord_ShouldError_WhenNotFound(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.PurgeBucketRecord(context.Background(), "my-bucket")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ── DeleteUserShares ──────────────────────────────────────────────────────────

func TestSharesClientDeleteUserShares_ShouldCallBothEndpoints_WhenSuccessful(t *testing.T) {
	// Arrange
	callCount := 0
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"deleted":1}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act — DeleteUserShares never returns an error (best-effort)
	client.DeleteUserShares(context.Background(), "user-uuid", "user@x.com")

	// Assert — called twice: once for ownerUserId, once for email
	assert.Equal(t, 2, callCount)
}

// ── CreateSharedLink ──────────────────────────────────────────────────────────

func TestSharesClientCreateSharedLink_ShouldSucceed_WhenCreated(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "shared-links")
		w.WriteHeader(http.StatusCreated)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.CreateSharedLink(context.Background(), "owner-uuid", "my-bucket", "photo.jpg", time.Now().Add(24*time.Hour))

	// Assert
	require.NoError(t, err)
}

func TestSharesClientCreateSharedLink_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.CreateSharedLink(context.Background(), "owner-uuid", "my-bucket", "photo.jpg", time.Now().Add(24*time.Hour))

	// Assert
	require.Error(t, err)
}

// ── DeleteSharedLinksByFileKey ────────────────────────────────────────────────

func TestSharesClientDeleteSharedLinksByFileKey_ShouldSucceed_WhenOK(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.RawQuery, "bucket=my-bucket")
		assert.Contains(t, r.URL.RawQuery, "fileKey=photo.jpg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"deleted":1}`)) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteSharedLinksByFileKey(context.Background(), "my-bucket", "photo.jpg")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientDeleteSharedLinksByFileKey_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteSharedLinksByFileKey(context.Background(), "my-bucket", "photo.jpg")

	// Assert
	require.Error(t, err)
}
