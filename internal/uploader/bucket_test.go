package uploader

import (
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
	}
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
