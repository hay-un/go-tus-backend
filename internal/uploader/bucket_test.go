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

// newTestApp creates an App with a fresh MockS3Client for bucket tests.
func newTestApp(mockS3 *MockS3Client) *App {
	return &App{
		S3Client:   mockS3,
		BucketName: "default-bucket",
		S3Endpoint: "http://localhost:9000",
	}
}

// ── ListBuckets ─────────────────────────────────────────────────────────────

func TestListBuckets_HappyPath(t *testing.T) {
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
	app.BucketsHandler(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var buckets []map[string]interface{}
	err := json.NewDecoder(rr.Body).Decode(&buckets)
	assert.NoError(t, err)
	assert.Len(t, buckets, 2)
	assert.Equal(t, "music", buckets[0]["name"])
	mockS3.AssertExpectations(t)
}

// ── CreateBucket ─────────────────────────────────────────────────────────────

func TestCreateBucket_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// HeadBucket → not found (bucket doesn't exist yet)
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-bucket"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "new-bucket"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"name":"new-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()
	app.BucketsHandler(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Contains(t, rr.Body.String(), "new-bucket")
	mockS3.AssertExpectations(t)
}

func TestCreateBucket_EmptyName(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"name":""}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()
	app.BucketsHandler(rr, req)

	// Must reject without calling S3
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	mockS3.AssertNotCalled(t, "HeadBucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestCreateBucket_DuplicateName(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// HeadBucket → bucket already exists (returns success)
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "existing-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"name":"existing-bucket"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets", strings.NewReader(body))
	rr := httptest.NewRecorder()
	app.BucketsHandler(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already exists")
	mockS3.AssertNotCalled(t, "CreateBucket")
	mockS3.AssertExpectations(t)
}

// ── DeleteBucket ─────────────────────────────────────────────────────────────

func TestDeleteBucket_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// Bucket exists
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-bucket"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	// Objects inside bucket
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
	app.BucketItemHandler(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteBucket_NotFound(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "ghost-bucket"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	req, _ := http.NewRequest(http.MethodDelete, "/buckets/ghost-bucket", nil)
	req.URL.Path = "/buckets/ghost-bucket"
	rr := httptest.NewRecorder()
	app.BucketItemHandler(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── RenameBucket ─────────────────────────────────────────────────────────────

func TestRenameBucket_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// Old bucket exists
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	// New bucket does NOT exist
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
	app.BucketItemHandler(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "new-name")
	mockS3.AssertExpectations(t)
}

func TestRenameBucket_NewNameExists(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// Old bucket exists
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	// New bucket ALSO exists → 409
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "taken-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"new_name":"taken-name"}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	rr := httptest.NewRecorder()
	app.BucketItemHandler(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "already exists")
	mockS3.AssertNotCalled(t, "CreateBucket")
	mockS3.AssertExpectations(t)
}

func TestRenameBucket_EmptyNewName(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	// Old bucket exists
	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "old-name"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"new_name":""}`
	req, _ := http.NewRequest(http.MethodPost, "/buckets/old-name/rename", strings.NewReader(body))
	req.URL.Path = "/buckets/old-name/rename"
	rr := httptest.NewRecorder()
	app.BucketItemHandler(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	mockS3.AssertNotCalled(t, "CreateBucket")
}
