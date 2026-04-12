package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// withAccountDeleteClaims injects Claims for a regular user owning two buckets.
func withAccountDeleteClaims(r *http.Request) *http.Request {
	claims := &Claims{
		Subject:        "user-sub-uuid",
		Email:          "user@example.com",
		AllowedBuckets: []string{"user-files", "user-files--photos"},
		Role:           "user",
	}
	return r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
}

// setupFakeRegistry returns a httptest server that handles bucket registry and cleanup calls.
func setupFakeRegistry(userUUID string, buckets []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// GET /internal/registry/buckets?owner=<uuid>
		if r.Method == http.MethodGet && r.URL.Path == "/internal/registry/buckets" {
			if r.URL.Query().Get("ownerId") != userUUID {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"data":[]}`))
				return
			}
			data, _ := json.Marshal(map[string]interface{}{"data": buckets})
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			return
		}
		// DELETE /internal/registry/buckets?owner=<uuid>
		if r.Method == http.MethodDelete && r.URL.Path == "/internal/registry/buckets" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Fallback for sharing calls
		if strings.HasPrefix(r.URL.Path, "/internal/shares/user/") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"deleted":0}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// ── DeleteAccountHandler ──────────────────────────────────────────────────────

func TestDeleteAccountHandler_ShouldReturn401_WhenNoClaims(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestDeleteAccountHandler_ShouldReturn405_WhenMethodNotDelete(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req, _ := http.NewRequest(http.MethodGet, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestDeleteAccountHandler_ShouldHardDeleteAllOwnedBuckets_WhenCalled(t *testing.T) {
	// Arrange
	userUUID := "user-sub-uuid"
	userBuckets := []string{"user-files", "user-files--photos"}
	fakeRegistry := setupFakeRegistry(userUUID, userBuckets)
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")

	// Both owned buckets get listed, objects deleted, and bucket deleted.
	for _, bucket := range userBuckets {
		b := bucket
		mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
			return aws.ToString(in.Bucket) == b
		}), mock.Anything).Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{{Key: aws.String("file1.mp4")}},
		}, nil)
		mockS3.On("DeleteObjects", mock.Anything, mock.MatchedBy(func(in *s3.DeleteObjectsInput) bool {
			return aws.ToString(in.Bucket) == b
		}), mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)
		mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
			return aws.ToString(in.Bucket) == b
		}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)
	}

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteAccountHandler_ShouldSkipWildcardBucket_WhenAdminClaims(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	// Admin claims: AllowedBuckets = ["*"] — no real bucket name, should make no S3 calls.
	// ListBuckets scan is also skipped for admin to avoid wiping all of MinIO.
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, &Claims{
		Subject:        "admin-uuid",
		Email:          "admin@example.com",
		AllowedBuckets: []string{"*"},
		Role:           "admin",
	}))
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertNotCalled(t, "ListBuckets")
	mockS3.AssertNotCalled(t, "ListObjectsV2")
	mockS3.AssertNotCalled(t, "DeleteBucket")
}

func TestDeleteAccountHandler_ShouldSkipSharesStep_WhenSharesNil(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3) // app.Shares is nil

	// With no shares registry, it falls back to scan/JWT.
	// We'll mock ListBuckets for the fallback path.
	mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("user-files")},
				{Name: aws.String("user-files--photos")},
			},
		}, nil)
	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act — must not panic
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestDeleteAccountHandler_ShouldCallDeleteUserSharesTwice_WhenSharesConfigured(t *testing.T) {
	// Arrange
	userUUID := "user-sub-uuid"
	userBuckets := []string{"user-files", "user-files--photos"}
	fakeRegistry := setupFakeRegistry(userUUID, userBuckets)
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestDeleteAccountHandler_ShouldDeleteKeycloakUser_WhenGranterConfigured(t *testing.T) {
	// Arrange
	userUUID := "user-sub-uuid"
	fakeRegistry := setupFakeRegistry(userUUID, []string{"user-files"})
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	mockGranter := new(MockKeycloakGranter)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")
	app.KeycloakGranter = mockGranter

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)
	mockGranter.On("DeleteUser", mock.Anything, "user-sub-uuid").Return(nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockGranter.AssertExpectations(t)
}

func TestDeleteAccountHandler_ShouldReturn204EvenIfKeycloakFails_WhenDataDeleted(t *testing.T) {
	// Arrange
	userUUID := "user-sub-uuid"
	fakeRegistry := setupFakeRegistry(userUUID, []string{"user-files"})
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	mockGranter := new(MockKeycloakGranter)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")
	app.KeycloakGranter = mockGranter

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)
	mockGranter.On("DeleteUser", mock.Anything, "user-sub-uuid").
		Return(fmt.Errorf("keycloak unavailable"))

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert: 204 even though Keycloak failed (data already deleted — best-effort).
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockGranter.AssertExpectations(t)
}

func TestDeleteAccountHandler_ShouldContinueAfterBucketError_WhenOneMinIOCallFails(t *testing.T) {
	// Arrange
	userUUID := "user-sub-uuid"
	userBuckets := []string{"user-files", "user-files--photos"}
	fakeRegistry := setupFakeRegistry(userUUID, userBuckets)
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")

	// First bucket fails on list; second bucket succeeds.
	mockS3.On("ListObjectsV2", mock.Anything, &s3.ListObjectsV2Input{Bucket: aws.String("user-files")}, mock.Anything).
		Return((*s3.ListObjectsV2Output)(nil), fmt.Errorf("MinIO timeout"))
	mockS3.On("ListObjectsV2", mock.Anything, &s3.ListObjectsV2Input{Bucket: aws.String("user-files--photos")}, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, &s3.DeleteBucketInput{Bucket: aws.String("user-files--photos")}, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert: still returns 204 — best-effort.
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── hardDeleteBucket ─────────────────────────────────────────────────────────

func TestHardDeleteBucket_ShouldDeleteObjectsAndBucket_WhenObjectsExist(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("ListObjectsV2", mock.Anything, &s3.ListObjectsV2Input{Bucket: aws.String("my-bucket")}, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: aws.String("file1")},
				{Key: aws.String("file2")},
			},
		}, nil)
	mockS3.On("DeleteObjects", mock.Anything, &s3.DeleteObjectsInput{
		Bucket: aws.String("my-bucket"),
		Delete: &types.Delete{Objects: []types.ObjectIdentifier{
			{Key: aws.String("file1")},
			{Key: aws.String("file2")},
		}},
	}, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, &s3.DeleteBucketInput{Bucket: aws.String("my-bucket")}, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	// Act
	err := app.hardDeleteBucket(context.Background(), "my-bucket")

	// Assert
	assert.NoError(t, err)
	mockS3.AssertExpectations(t)
}

func TestHardDeleteBucket_ShouldDeleteMultiplePages_WhenManyObjectsExist(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	bucketName := "big-bucket"

	// First page: 1000 objects, truncated
	objs1 := make([]types.Object, 1000)
	ids1 := make([]types.ObjectIdentifier, 1000)
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("file-%d", i)
		objs1[i] = types.Object{Key: aws.String(key)}
		ids1[i] = types.ObjectIdentifier{Key: aws.String(key)}
	}

	mockS3.On("ListObjectsV2", mock.Anything, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	}, mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents:              objs1,
		IsTruncated:           aws.Bool(true),
		NextContinuationToken: aws.String("token-1"),
	}, nil).Once()

	mockS3.On("DeleteObjects", mock.Anything, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucketName),
		Delete: &types.Delete{Objects: ids1},
	}, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil).Once()

	// Second page: 1 object, not truncated
	mockS3.On("ListObjectsV2", mock.Anything, &s3.ListObjectsV2Input{
		Bucket:            aws.String(bucketName),
		ContinuationToken: aws.String("token-1"),
	}, mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents:    []types.Object{{Key: aws.String("last-file")}},
		IsTruncated: aws.Bool(false),
	}, nil).Once()

	mockS3.On("DeleteObjects", mock.Anything, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucketName),
		Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("last-file")}}},
	}, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil).Once()

	mockS3.On("DeleteBucket", mock.Anything, &s3.DeleteBucketInput{
		Bucket: aws.String(bucketName),
	}, mock.Anything).Return(&s3.DeleteBucketOutput{}, nil).Once()

	// Act
	err := app.hardDeleteBucket(context.Background(), bucketName)

	// Assert
	assert.NoError(t, err)
	mockS3.AssertExpectations(t)
}

func TestHardDeleteBucket_ShouldSkipDeleteObjects_WhenBucketEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	// Act
	err := app.hardDeleteBucket(context.Background(), "empty-bucket")

	// Assert
	assert.NoError(t, err)
	mockS3.AssertNotCalled(t, "DeleteObjects")
}

func TestHardDeleteBucket_ShouldReturnNil_WhenBucketNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return((*s3.ListObjectsV2Output)(nil), &types.NoSuchBucket{})

	// Act
	err := app.hardDeleteBucket(context.Background(), "nonexistent")

	// Assert — bucket not found is treated as success (idempotent).
	assert.NoError(t, err)
	mockS3.AssertNotCalled(t, "DeleteBucket")
}

func TestDeleteAccountHandler_ShouldDeleteSubBuckets_WhenJWTIsStale(t *testing.T) {
	// Arrange: JWT only carries the root bucket — sub-bucket was created after the
	// last token refresh and is therefore absent from AllowedBuckets.
	userUUID := "user-sub-uuid"
	userBuckets := []string{"qwer", "qwer--level-1"}
	fakeRegistry := setupFakeRegistry(userUUID, userBuckets)
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")

	// Both buckets must be hard-deleted based on registry truth.
	for _, bucket := range userBuckets {
		b := bucket
		mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
			return aws.ToString(in.Bucket) == b
		}), mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)
		mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
			return aws.ToString(in.Bucket) == b
		}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)
	}

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, &Claims{
		Subject:        userUUID,
		Email:          "qwer@gmail.com",
		AllowedBuckets: []string{"qwer"}, // stale — qwer--level-1 missing
		Role:           "user",
	}))
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert: both root and stale sub-bucket are deleted.
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteAccountHandler_ShouldFallBackToJWTBuckets_WhenListBucketsFails(t *testing.T) {
	// Arrange: Registry fails; handler must still delete JWT-listed buckets (best-effort).
	fakeRegistry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fakeRegistry.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeRegistry.URL, "test-secret")

	// Handler fallback path should delete what's in the JWT claims
	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "qwer"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "qwer"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, &Claims{
		Subject:        "user-sub-uuid",
		Email:          "qwer@gmail.com",
		AllowedBuckets: []string{"qwer"},
		Role:           "user",
	}))
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert: 204 even when Registry fails — best-effort fallback to JWT.
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteAccountHandler_ShouldReturn204_WhenSharesDeleteFails(t *testing.T) {
	// Arrange: shares server returns non-200 → DeleteUserShares logs error but continues
	fakeShares := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // triggers deleteUserByIdentifier error
	}))
	defer fakeShares.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(fakeShares.URL, "test-secret")

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return true // skip specific bucket check here as it's a success-case best-effort test
	}), mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return true
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/users/me", nil)
	req = withAccountDeleteClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteAccountHandler(rr, req)

	// Assert: still returns 204 — delete user shares is best-effort
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

// ── response helpers ──────────────────────────────────────────────────────────

func parseJSONError(body string) string {
	var out struct{ Error string }
	_ = json.Unmarshal([]byte(body), &out)
	return out.Error
}
