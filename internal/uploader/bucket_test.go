package uploader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newTestApp creates an App with a MockS3Client — shared helper for all bucket tests.
func newTestApp(mockS3 *MockS3Client) *App {
	return &App{
		S3Client:   mockS3,
		BucketName: "default-bucket",
		S3Endpoint: "http://localhost:9000",
		Audit:      &NoopAuditProducer{},
	}
}

// withAdminClaims injects admin Claims into the request context.
func withAdminClaims(r *http.Request) *http.Request {
	claims := &Claims{Subject: "admin-uuid", Email: "admin@test.com", AllowedBuckets: []string{"*"}, Role: "admin"}
	return r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
}

// withUserClaims injects restricted user Claims into the request context.
func withUserClaims(r *http.Request, allowedBuckets []string) *http.Request {
	claims := &Claims{Subject: "user-uuid", Email: "user@test.com", AllowedBuckets: allowedBuckets, Role: "user"}
	return r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
}

// ── ListBuckets ──────────────────────────────────────────────────────────────

func TestListBucketsHandler_ShouldReturnAllBuckets_WhenBucketsExist(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("music"), CreationDate: &created},
				{Name: aws.String("photos"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	var buckets []map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&buckets))
	assert.Len(t, buckets, 2)
	assert.Equal(t, "music", buckets[0]["name"])
	mockS3.AssertExpectations(t)
}

// ── CreateBucket ─────────────────────────────────────────────────────────────

func TestCreateBucketHandler_ShouldReturn201WithCreatedAt_WhenNameIsAvailable(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-bucket"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-bucket"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"name":"new-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "new-bucket", resp["name"])
	assert.NotEmpty(t, resp["created_at"], "created_at must be present so frontend can render the date immediately without a refresh")
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldReturn400_WhenNameIsEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":""}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	mockS3.AssertNotCalled(t, "HeadBucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestCreateBucketHandler_ShouldReturn409_WhenBucketAlreadyExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "existing-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"name":"existing-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already exists")
	mockS3.AssertNotCalled(t, "CreateBucket")
	mockS3.AssertExpectations(t)
}

// ── DeleteBucket ─────────────────────────────────────────────────────────────

func TestDeleteBucketHandler_ShouldReturn204AndPurgeObjects_WhenBucketExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "old-bucket"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("file1.mp3")},
			{Key: aws.String("file2.mp3")},
		},
	}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteObjectsOutput{}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-bucket"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/old-bucket", nil)
	req.URL.Path = "/buckets/old-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldCascadeDeleteShares_WhenSharesConfigured(t *testing.T) {
	// Arrange: stub go-shares server that records DELETE /internal/shares/bucket/{bucket}
	var deletedBucket string
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/internal/shares/bucket/") {
			deletedBucket = strings.TrimPrefix(r.URL.Path, "/internal/shares/bucket/")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"deleted":1}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sharesServer.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/shared-bucket", nil)
	req.URL.Path = "/buckets/shared-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "shared-bucket", deletedBucket)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldStillReturn204_WhenSharesDeletionFails(t *testing.T) {
	// Arrange: stub go-shares server that returns an error
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sharesServer.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/shared-bucket", nil)
	req.URL.Path = "/buckets/shared-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert: shares deletion failure is best-effort — bucket delete still succeeds
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn404_WhenBucketNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "ghost-bucket"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/ghost-bucket", nil)
	req.URL.Path = "/buckets/ghost-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── RenameBucket ─────────────────────────────────────────────────────────────

func TestRenameBucketHandler_ShouldCopyObjectsAndDeleteOldBucket_WhenBothNamesAreValid(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("song.mp3")},
		},
	}, nil)

	mockS3.On("CopyObject", mock.Anything, mock.MatchedBy(func(in *s3.CopyObjectInput) bool {
		return aws.ToString(in.Bucket) == "new-name" && aws.ToString(in.Key) == "song.mp3"
	}), mock.Anything).Return(&s3.CopyObjectOutput{}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.MatchedBy(func(in *s3.DeleteObjectsInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "new-name")
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn409_WhenNewNameAlreadyExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "taken-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"new_name":"taken-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already exists")
	mockS3.AssertNotCalled(t, "CreateBucket")
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn400_WhenNewNameIsEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"new_name":""}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	mockS3.AssertNotCalled(t, "CreateBucket")
}

// ── Access control ────────────────────────────────────────────────────────────

func TestListBucketsHandler_ShouldFilterBuckets_WhenAllowedBucketsRestricted(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("allowed-bucket"), CreationDate: &created},
				{Name: aws.String("other-bucket"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	req = withUserClaims(req, []string{"allowed-bucket"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	var buckets []map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&buckets))
	assert.Len(t, buckets, 1, "only the allowed bucket should appear")
	assert.Equal(t, "allowed-bucket", buckets[0]["name"])
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldReturn403_WhenNonAdmin(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":"new-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = withUserClaims(req, []string{"*"}) // AllowedBuckets=*, but Role=user
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "admin role required")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestDeleteBucketHandler_ShouldReturn403_WhenUserDoesNotOwnBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	req = withUserClaims(req, []string{"other-bucket"}) // does not own "my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "you do not own this bucket")
	mockS3.AssertNotCalled(t, "DeleteBucket")
}

func TestDeleteBucketHandler_ShouldReturn204_WhenUserOwnsBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{Contents: nil}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	req = withUserClaims(req, []string{"my-bucket"}) // owns "my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn403_WhenUserDoesNotOwnBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = withUserClaims(req, []string{"other-bucket"}) // does not own "old-name"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "you do not own this bucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestRenameBucketHandler_ShouldReturn200_WhenUserOwnsBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: nil}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/my-bucket/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/my-bucket/rename"
	req = withUserClaims(req, []string{"my-bucket"}) // owns "my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "new-name")
	mockS3.AssertExpectations(t)
}

// ── Sub-bucket creation ───────────────────────────────────────────────────────

func TestCreateBucketHandler_ShouldReturn201WithFullName_WhenUserOwnsParentBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "john-files--work"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "john-files--work"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"name":"work","parent":"john-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = withUserClaims(req, []string{"john-files"}) // owns "john-files"
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "john-files--work", resp["name"], "response name must be the full MinIO bucket name")
	assert.NotEmpty(t, resp["created_at"])
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldReturn403_WhenUserDoesNotOwnParentBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":"work","parent":"alice-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = withUserClaims(req, []string{"john-files"}) // owns john-files, NOT alice-files
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "you do not own the parent bucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestCreateBucketHandler_ShouldReturn401_WhenNoAuthAndParentProvided(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":"work","parent":"john-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	// no claims injected — simulates missing/invalid JWT
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "authentication required")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

// TestListBucketsHandler_ShouldIncludeSubBucket_WhenUserOwnsParentBucket guards the
// regression where "parent--child" disappeared from the list after logout/login.
// Owning "john-files" must implicitly grant access to "john-files--level-1" via the
// HasPrefix check in CanAccessBucket — no explicit grant in the JWT is required.
func TestListBucketsHandler_ShouldIncludeSubBucket_WhenUserOwnsParentBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("john-files"), CreationDate: &created},
				{Name: aws.String("john-files--level-1"), CreationDate: &created},
				{Name: aws.String("alice-files"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	// JWT only has the parent bucket — sub-bucket is NOT explicitly listed
	req = withUserClaims(req, []string{"john-files"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	var buckets []map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&buckets))

	names := make([]string, 0, len(buckets))
	for _, b := range buckets {
		names = append(names, b["name"].(string))
	}
	assert.Contains(t, names, "john-files", "parent bucket must be present")
	assert.Contains(t, names, "john-files--level-1", "sub-bucket must be visible via parent ownership")
	assert.NotContains(t, names, "alice-files", "other user's bucket must be filtered out")
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldCallGrantBucket_WhenCreatingSubBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	mockGranter := new(MockKeycloakGranter)
	app.KeycloakGranter = mockGranter

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "john-files--level-1"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "john-files--level-1"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	mockGranter.On("GrantBucket", mock.Anything, "user@test.com", "john-files--level-1").
		Return(nil)

	body := `{"name":"level-1","parent":"john-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = withUserClaims(req, []string{"john-files"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	mockS3.AssertExpectations(t)
	mockGranter.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldNotCallGrantBucket_WhenCreatingTopLevelBucket(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	mockGranter := new(MockKeycloakGranter)
	app.KeycloakGranter = mockGranter

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-top-level"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-top-level"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"name":"new-top-level"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = withAdminClaims(req) // top-level creation requires admin
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	mockGranter.AssertNotCalled(t, "GrantBucket")
	mockS3.AssertExpectations(t)
}
