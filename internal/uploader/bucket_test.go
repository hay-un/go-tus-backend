package uploader

import (
	"context"
	"encoding/json"
	"errors"
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

func TestDeleteBucketHandler_ShouldSoftDelete_WhenSharesConfigured(t *testing.T) {
	// Arrange: stub go-shares server that accepts POST /internal/buckets/trash (soft delete)
	var trashedBucket string
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/buckets/trash" {
			var body struct {
				BucketName string `json:"bucketName"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			trashedBucket = body.BucketName
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"test-id","bucketName":"shared-bucket"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sharesServer.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	// Soft delete only needs to check that the bucket exists in MinIO
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "shared-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/shared-bucket", nil)
	req.URL.Path = "/buckets/shared-bucket"
	// Owner must be authenticated for soft delete
	req = withUserClaims(req, []string{"shared-bucket"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert: soft delete → 204, MinIO bucket NOT deleted yet (stays until purge job runs)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "shared-bucket", trashedBucket)
	mockS3.AssertNotCalled(t, "DeleteBucket")
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn401_WhenSharesConfiguredButNoAuth(t *testing.T) {
	// Arrange: go-shares configured but no JWT claims in request
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer sharesServer.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/some-bucket", nil)
	req.URL.Path = "/buckets/some-bucket"
	// No claims injected
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert: soft delete requires authentication
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	mockS3.AssertNotCalled(t, "HeadBucket")
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

func TestDeleteBucketHandler_ShouldReturn500_WhenBucketExistsCheckFails(t *testing.T) {
	// Arrange — hard delete (Shares=nil); HeadBucket returns generic error
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3 internal error"))

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn500_WhenListObjectsFails(t *testing.T) {
	// Arrange — hard delete (Shares=nil); bucket found, but list objects fails
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("list failed"))

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn500_WhenDeleteObjectsFails(t *testing.T) {
	// Arrange — hard delete; bucket has objects, DeleteObjects fails
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{{Key: aws.String("file.mp3")}},
		}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("delete objects failed"))

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn500_WhenDeleteBucketFails(t *testing.T) {
	// Arrange — hard delete; bucket empty, DeleteBucket fails
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: nil}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("delete bucket failed"))

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucketHandler_ShouldReturn500_WhenSoftDeleteBucketExistsCheckFails(t *testing.T) {
	// Arrange — soft delete (Shares configured); HeadBucket returns generic error
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer sharesServer.Close()

	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "test-secret")

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3 internal error"))

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req.URL.Path = "/buckets/my-bucket"
	req = withUserClaims(req, []string{"my-bucket"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldReturn500_WhenCreateBucketFails(t *testing.T) {
	// Arrange — admin creates top-level bucket; CreateBucket fails
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("create failed"))

	body := `{"name":"new-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
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

func TestRenameBucketHandler_ShouldReturn404_WhenOldBucketNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn400_WhenNewNameSameAsOld(t *testing.T) {
	// Arrange — same-name check fires before any S3 call
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"new_name":"old-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "must differ")
	mockS3.AssertNotCalled(t, "HeadBucket")
}

func TestRenameBucketHandler_ShouldReturn500_WhenCreateBucketFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("create failed"))

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn500_WhenListObjectsFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("list failed"))

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn500_WhenCopyObjectFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{{Key: aws.String("song.mp3")}},
		}, nil)

	mockS3.On("CopyObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("copy failed"))

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn500_WhenDeleteObjectsFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{{Key: aws.String("song.mp3")}},
		}, nil)

	mockS3.On("CopyObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CopyObjectOutput{}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("delete objects failed"))

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestRenameBucketHandler_ShouldReturn500_WhenDeleteBucketFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-name"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: nil}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("delete bucket failed"))

	body := `{"new_name":"new-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"old-name"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
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

// TestCreateBucketHandler_ShouldAllowSubBucket_WhenJWTHasNoAllowedBucketsButParentIsUsersDefault
// guards the race condition where allowedBuckets is empty in the JWT right after first-login
// provisioning (Keycloak attribute not yet propagated), but the user's default bucket exists in MinIO.
func TestCreateBucketHandler_ShouldAllowSubBucket_WhenJWTHasNoAllowedBucketsButParentIsUsersDefault(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// bucketExists check for the parent (fallback ownership path)
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "ridho-files"
	}), mock.Anything).Return(nil, nil)

	// bucketExists check for the new sub-bucket (duplicate check)
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "ridho-files--work"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "ridho-files--work"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"name":"work","parent":"ridho-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	// Stale JWT: AllowedBuckets is empty (not yet propagated after first-login provisioning)
	claims := &Claims{Subject: "ridho-uuid", Email: "ridho@gmail.com", AllowedBuckets: []string{}, Role: "user"}
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, claims))
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "ridho-files--work", resp["name"])
	mockS3.AssertExpectations(t)
}

func TestCreateBucketHandler_ShouldReturn403_WhenJWTEmptyAndEmailDoesNotMatchParent(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":"work","parent":"ridho-files"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	// Different email: other@gmail.com → expected bucket is "other-files", not "ridho-files"
	claims := &Claims{Subject: "other-uuid", Email: "other@gmail.com", AllowedBuckets: []string{}, Role: "user"}
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, claims))
	rr := httptest.NewRecorder()

	// Act
	app.BucketsHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "you do not own the parent bucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

// ── BucketsHandler default method ────────────────────────────────────────────

func TestBucketsHandler_ShouldReturn405_WhenMethodNotAllowed(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	req, _ := http.NewRequest(http.MethodDelete, "/buckets", nil)
	rr := httptest.NewRecorder()
	app.BucketsHandler(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// ── ListBucketsHandler with Shares ────────────────────────────────────────────

func TestListBucketsHandler_ShouldExcludeTrashedBuckets_WhenSharesConfigured(t *testing.T) {
	// Arrange — go-shares says "music" is in trash, "photos" is not
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trash") && !strings.Contains(r.URL.Path, "/expired") && !strings.Contains(r.URL.Path, "purge") {
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"data": []map[string]interface{}{
					{"bucketName": "music"},
				},
			})
			return
		}
		// shared-buckets endpoint
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "sec")

	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("music"), CreationDate: &created},
				{Name: aws.String("photos"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	req = withAdminClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.ListBucketsHandler(rr, req)

	// Assert — "music" is trashed, only "photos" should appear
	assert.Equal(t, http.StatusOK, rr.Code)
	var buckets []map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&buckets))
	assert.Len(t, buckets, 1)
	assert.Equal(t, "photos", buckets[0]["name"])
}

func TestListBucketsHandler_ShouldIncludeSharedBuckets_WhenSharesConfigured(t *testing.T) {
	// Arrange — go-shares returns "shared-bucket" as shared with user
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trash") {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
			return
		}
		// /internal/shares/shared-buckets endpoint
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []map[string]interface{}{
				{"ownerBucket": "shared-bucket"},
			},
		})
	}))
	defer sharesServer.Close()

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "sec")

	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("my-bucket"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	req = withAdminClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.ListBucketsHandler(rr, req)

	// Assert — both "my-bucket" and "shared-bucket" appear
	assert.Equal(t, http.StatusOK, rr.Code)
	var buckets []map[string]interface{}
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&buckets))
	assert.Len(t, buckets, 2)
}

func TestListBucketsHandler_ShouldReturn200_WhenGetSharedBucketsReturnsNonJSON(t *testing.T) {
	// Arrange — go-shares returns non-JSON for shared-buckets; handler silently ignores it
	// but this covers the decode-error path inside GetSharedBuckets
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trash") {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer sharesServer.Close()

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "sec")

	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("my-bucket"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	req = withAdminClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.ListBucketsHandler(rr, req)

	// Assert — still 200; error from GetSharedBuckets is silently ignored
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestListBucketsHandler_ShouldReturn200_WhenGetSharedBucketsReturnsNullData(t *testing.T) {
	// Arrange — shares server returns {"data":null} — covers body.Data == nil branch
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trash") {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":null}`)) //nolint:errcheck
	}))
	defer sharesServer.Close()

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	app.Shares = NewSharesClient(sharesServer.URL, "sec")

	mockS3.On("ListBuckets", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListBucketsOutput{
			Buckets: []types.Bucket{
				{Name: aws.String("my-bucket"), CreationDate: &created},
			},
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/buckets", nil)
	req = withAdminClaims(req)
	rr := httptest.NewRecorder()

	// Act
	app.ListBucketsHandler(rr, req)

	// Assert — still 200; empty shared buckets list returned
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── deleteBucketHandler with Shares (soft-delete) ────────────────────────────

func newDeleteBucketApp(sharesServerURL string, mockS3 *MockS3Client) *App {
	a := newTestApp(mockS3)
	a.Shares = NewSharesClient(sharesServerURL, "sec")
	return a
}

func TestDeleteBucketHandler_ShouldReturn401_WhenSharesSetAndNoClaims(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	mockS3 := new(MockS3Client)
	app := newDeleteBucketApp(srv.URL, mockS3)

	req := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	// no claims
	w := httptest.NewRecorder()
	app.deleteBucketHandler(w, req, "my-bucket")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestDeleteBucketHandler_ShouldReturn404_WhenSharesSetAndBucketNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	mockS3 := new(MockS3Client)
	app := newDeleteBucketApp(srv.URL, mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "ghost-bucket"
	}), mock.Anything).Return(nil, &types.NotFound{})

	req := httptest.NewRequest(http.MethodDelete, "/buckets/ghost-bucket", nil)
	req = injectClaims(req, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"ghost-bucket"}, Role: "user"})
	w := httptest.NewRecorder()
	app.deleteBucketHandler(w, req, "ghost-bucket")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteBucketHandler_ShouldReturn409_WhenBucketAlreadyInTrash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TrashBucket POST → 409
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "bucket is already in trash"}) //nolint:errcheck
	}))
	defer srv.Close()
	mockS3 := new(MockS3Client)
	app := newDeleteBucketApp(srv.URL, mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req = injectClaims(req, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()
	app.deleteBucketHandler(w, req, "my-bucket")
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDeleteBucketHandler_ShouldReturn204_WhenTrashSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TrashBucket POST → 201
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{"bucketName": "my-bucket"}) //nolint:errcheck
	}))
	defer srv.Close()
	mockS3 := new(MockS3Client)
	app := newDeleteBucketApp(srv.URL, mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req = injectClaims(req, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()
	app.deleteBucketHandler(w, req, "my-bucket")
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestDeleteBucketHandler_ShouldReturn500_WhenTrashFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	mockS3 := new(MockS3Client)
	app := newDeleteBucketApp(srv.URL, mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/buckets/my-bucket", nil)
	req = injectClaims(req, &Claims{Subject: "owner-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()
	app.deleteBucketHandler(w, req, "my-bucket")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── ListBucketTrashHandler ───────────────────────────────────────────────────

func TestListBucketTrashHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	r := httptest.NewRequest(http.MethodGet, "/buckets/trash", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", Email: "user@test.com"})
	w := httptest.NewRecorder()

	// Act
	app.ListBucketTrashHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestListBucketTrashHandler_ShouldReturn401_WhenNoClaims(t *testing.T) {
	// Arrange
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer sharesServer.Close()
	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodGet, "/buckets/trash", nil)
	// no claims injected
	w := httptest.NewRecorder()

	// Act
	app.ListBucketTrashHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestListBucketTrashHandler_ShouldReturn200_WhenAuthorized(t *testing.T) {
	// Arrange — go-shares returns empty list
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodGet, "/buckets/trash", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", Email: "user@test.com"})
	w := httptest.NewRecorder()

	// Act
	app.ListBucketTrashHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestListBucketTrashHandler_ShouldReturn500_WhenSharesFails(t *testing.T) {
	// Arrange — go-shares returns non-JSON (causes decode error in GetTrashedBuckets)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodGet, "/buckets/trash", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", Email: "user@test.com"})
	w := httptest.NewRecorder()

	// Act
	app.ListBucketTrashHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── RestoreBucketHandler ─────────────────────────────────────────────────────

func TestRestoreBucketHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.RestoreBucketHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRestoreBucketHandler_ShouldReturn403_WhenNotOwner(t *testing.T) {
	// Arrange
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer sharesServer.Close()
	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodPost, "/buckets/other-bucket/restore", nil)
	// user only owns "my-bucket", not "other-bucket"
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.RestoreBucketHandler(w, r, "other-bucket")

	// Assert
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRestoreBucketHandler_ShouldReturn204_WhenRestored(t *testing.T) {
	// Arrange — go-shares returns 204
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.RestoreBucketHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestRestoreBucketHandler_ShouldReturn404_WhenNotInTrash(t *testing.T) {
	// Arrange — go-shares returns 404
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found in trash"}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.RestoreBucketHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRestoreBucketHandler_ShouldReturn500_WhenRestoreFails(t *testing.T) {
	// Arrange — go-shares returns 500
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	r := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-uuid", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.RestoreBucketHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── canAccessBucket ──────────────────────────────────────────────────────────

func TestCanAccessBucket_ShouldReturnFalse_WhenBucketIsInTrash(t *testing.T) {
	// Arrange — go-shares reports bucket is deleted
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"deleted": true}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	claims := &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"my-bucket"}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "my-bucket")

	// Assert
	assert.False(t, result)
}

func TestCanAccessBucket_ShouldReturnTrue_WhenHomeBucket(t *testing.T) {
	// Arrange — claims have no explicit allowedBuckets, but email matches home bucket
	app := &App{Audit: &NoopAuditProducer{}} // Shares=nil
	claims := &Claims{Subject: "u", Email: "ridho@gmail.com", AllowedBuckets: []string{}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "ridho-files")

	// Assert
	assert.True(t, result)
}

func TestCanAccessBucket_ShouldReturnFalse_WhenSharesNilAndNoMatch(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}} // Shares=nil
	claims := &Claims{Subject: "u", Email: "ridho@gmail.com", AllowedBuckets: []string{}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "other-bucket")

	// Assert
	assert.False(t, result)
}

func TestCanAccessBucket_ShouldReturnTrue_WhenSharesGrantsAccess(t *testing.T) {
	// Arrange — go-shares says user has access via share
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/deleted") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"deleted": false}) //nolint:errcheck
			return
		}
		// /check endpoint
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"hasAccess": true, "permission": "read"}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	// User has no JWT allowedBuckets for this bucket
	claims := &Claims{Subject: "sharee-uuid", Email: "sharee@test.com", AllowedBuckets: []string{}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "owner-bucket")

	// Assert
	assert.True(t, result)
}

func TestCanAccessBucket_ShouldReturnFalse_WhenSharesCheckFails(t *testing.T) {
	// Arrange — go-shares check endpoint returns error
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/deleted") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"deleted": false}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	claims := &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "owner-bucket")

	// Assert
	assert.False(t, result) // fail closed
}

func TestCanAccessBucket_ShouldReturnFalse_WhenEmailEmpty(t *testing.T) {
	// Arrange — shares is set but email is empty → short-circuit before shares check
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/deleted") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"deleted": false}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sharesServer.Close()

	app := &App{Audit: &NoopAuditProducer{}, Shares: NewSharesClient(sharesServer.URL, "sec")}
	claims := &Claims{Subject: "u", Email: "", AllowedBuckets: []string{}, Role: "user"}

	// Act
	result := app.canAccessBucket(context.Background(), claims, "owner-bucket")

	// Assert
	assert.False(t, result)
}
