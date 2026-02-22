package uploader

import (
	"context"
	"errors"
	"io"
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

// MockS3Client matches the s3store.S3API interface
type MockS3Client struct {
	mock.Mock
}

func (m *MockS3Client) PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.PutObjectOutput), args.Error(1)
}

func (m *MockS3Client) GetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.GetObjectOutput), args.Error(1)
}

func (m *MockS3Client) ListParts(ctx context.Context, input *s3.ListPartsInput, optFns ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.ListPartsOutput), args.Error(1)
}

func (m *MockS3Client) CreateMultipartUpload(ctx context.Context, input *s3.CreateMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.CreateMultipartUploadOutput), args.Error(1)
}
func (m *MockS3Client) CompleteMultipartUpload(ctx context.Context, input *s3.CompleteMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.CompleteMultipartUploadOutput), args.Error(1)
}
func (m *MockS3Client) AbortMultipartUpload(ctx context.Context, input *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.AbortMultipartUploadOutput), args.Error(1)
}
func (m *MockS3Client) UploadPart(ctx context.Context, input *s3.UploadPartInput, optFns ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.UploadPartOutput), args.Error(1)
}
func (m *MockS3Client) ListObjectsV2(ctx context.Context, input *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.ListObjectsV2Output), args.Error(1)
}
func (m *MockS3Client) HeadObject(ctx context.Context, input *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.HeadObjectOutput), args.Error(1)
}
func (m *MockS3Client) DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.DeleteObjectOutput), args.Error(1)
}
func (m *MockS3Client) DeleteObjects(ctx context.Context, input *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.DeleteObjectsOutput), args.Error(1)
}

func (m *MockS3Client) UploadPartCopy(ctx context.Context, input *s3.UploadPartCopyInput, optFns ...func(*s3.Options)) (*s3.UploadPartCopyOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.UploadPartCopyOutput), args.Error(1)
}

func (m *MockS3Client) CreateBucket(ctx context.Context, input *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.CreateBucketOutput), args.Error(1)
}

func (m *MockS3Client) DeleteBucket(ctx context.Context, input *s3.DeleteBucketInput, optFns ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.DeleteBucketOutput), args.Error(1)
}

func (m *MockS3Client) ListBuckets(ctx context.Context, input *s3.ListBucketsInput, optFns ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.ListBucketsOutput), args.Error(1)
}

func (m *MockS3Client) HeadBucket(ctx context.Context, input *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.HeadBucketOutput), args.Error(1)
}

func (m *MockS3Client) CopyObject(ctx context.Context, input *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	args := m.Called(ctx, input, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.CopyObjectOutput), args.Error(1)
}

// ── TUS handler creation ──────────────────────────────────────────────────────

func TestNewHandler_Creation(t *testing.T) {
	mockS3 := new(MockS3Client)

	h, err := NewTusHandler("test-bucket", mockS3)
	assert.NoError(t, err)
	assert.NotNil(t, h)
}

func TestTusCreation_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	h, _ := NewTusHandler("test-bucket", mockS3)

	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CreateMultipartUploadOutput{
		UploadId: aws.String("upload-id-123"),
		Bucket:   aws.String("test-bucket"),
		Key:      aws.String("test-file"),
	}, nil)

	mockS3.On("PutObject", mock.Anything, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return *input.Bucket == "test-bucket" && strings.HasSuffix(*input.Key, ".info")
	}), mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	mux := http.NewServeMux()
	mux.Handle("/files/", h)

	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	req.Header.Set("Upload-Metadata", "filename dGVzdC50eHQ=")

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Location"))
	mockS3.AssertExpectations(t)
}

func TestTusCreation_StorageFailure(t *testing.T) {
	mockS3 := new(MockS3Client)
	h, _ := NewTusHandler("test-bucket", mockS3)

	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("s3: ServiceUnavailable"))

	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.NotEqual(t, http.StatusCreated, rr.Code)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── ListFilesHandler ──────────────────────────────────────────────────────────

func TestListFiles_RequiresBucketParam(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	req, _ := http.NewRequest("GET", "/files/", nil)
	rr := httptest.NewRecorder()
	app.ListFilesHandler(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "bucket query parameter is required")
	mockS3.AssertNotCalled(t, "ListObjectsV2")
}

func TestListFiles_WithBucketParam(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(input *s3.ListObjectsV2Input) bool {
		return aws.ToString(input.Bucket) == "my-videos"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("abc123"), Size: aws.Int64(1000)},
			{Key: aws.String("abc123.info"), Size: aws.Int64(100)},
		},
	}, nil)

	infoContent := `{"MetaData": {"filename": "movie.mp4"}}`
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Bucket) == "my-videos" && aws.ToString(input.Key) == "abc123.info"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(infoContent)),
	}, nil)

	req, _ := http.NewRequest("GET", "/files/?bucket=my-videos", nil)
	rr := httptest.NewRecorder()
	app.ListFilesHandler(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "movie.mp4")
	assert.Contains(t, rr.Body.String(), "abc123")
	assert.Contains(t, rr.Body.String(), "/files/my-videos/abc123")
	// .info sidecar must NOT appear in the list
	assert.NotContains(t, rr.Body.String(), "abc123.info")
	mockS3.AssertExpectations(t)
}

func TestListFiles_BucketNotFound(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})

	req, _ := http.NewRequest("GET", "/files/?bucket=ghost-bucket", nil)
	rr := httptest.NewRecorder()
	app.ListFilesHandler(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── DownloadFileHandler ───────────────────────────────────────────────────────

func TestDownloadFileHandler_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	fileContent := "hello world"

	infoContent := `{"MetaData": {"filename": "hello.txt"}}`
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "abc123.info"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(infoContent)),
	}, nil)

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "abc123" && aws.ToString(input.Bucket) == "my-bucket"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(fileContent)),
		ContentType:   aws.String("text/plain"),
		ContentLength: aws.Int64(int64(len(fileContent))),
	}, nil)

	req, _ := http.NewRequest("GET", "/files/my-bucket/abc123", nil)
	rr := httptest.NewRecorder()
	app.DownloadFileHandler(rr, req, "my-bucket", "abc123")

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, fileContent, rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Disposition"), "hello.txt")
	assert.Equal(t, "text/plain", rr.Header().Get("Content-Type"))
	mockS3.AssertExpectations(t)
}

func TestDownloadFileHandler_FileNotFound(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	// .info not found
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "ghost.info"
	}), mock.Anything).Return(nil, &types.NoSuchKey{})

	// Actual file not found
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "ghost"
	}), mock.Anything).Return(nil, &types.NoSuchKey{})

	req, _ := http.NewRequest("GET", "/files/my-bucket/ghost", nil)
	rr := httptest.NewRecorder()
	app.DownloadFileHandler(rr, req, "my-bucket", "ghost")

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertExpectations(t)
}

// ── DeleteFileHandler ─────────────────────────────────────────────────────────

func TestDeleteFileHandler_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(input *s3.HeadObjectInput) bool {
		return aws.ToString(input.Bucket) == "my-bucket" && aws.ToString(input.Key) == "abc123"
	}), mock.Anything).Return(&s3.HeadObjectOutput{}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.MatchedBy(func(input *s3.DeleteObjectsInput) bool {
		if aws.ToString(input.Bucket) != "my-bucket" {
			return false
		}
		keys := make(map[string]bool)
		for _, obj := range input.Delete.Objects {
			keys[aws.ToString(obj.Key)] = true
		}
		return keys["abc123"] && keys["abc123.info"]
	}), mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	req, _ := http.NewRequest("DELETE", "/files/my-bucket/abc123", nil)
	rr := httptest.NewRecorder()
	app.DeleteFileHandler(rr, req, "my-bucket", "abc123")

	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteFileHandler_FileNotFound(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000"}

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(input *s3.HeadObjectInput) bool {
		return aws.ToString(input.Key) == "ghost"
	}), mock.Anything).Return(nil, &types.NotFound{})

	req, _ := http.NewRequest("DELETE", "/files/my-bucket/ghost", nil)
	rr := httptest.NewRecorder()
	app.DeleteFileHandler(rr, req, "my-bucket", "ghost")

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertNotCalled(t, "DeleteObjects")
	mockS3.AssertExpectations(t)
}

// ── ExtractBucketFromTUSMetadata ──────────────────────────────────────────────

func TestExtractBucketFromTUSMetadata(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "bucket present with filename",
			header:   "filename dGVzdC50eHQ=,bucket bXktYnVja2V0",
			expected: "my-bucket",
		},
		{
			name:     "bucket only",
			header:   "bucket bXktdmlkZW9z",
			expected: "my-videos",
		},
		{
			name:     "no bucket field",
			header:   "filename dGVzdC50eHQ=",
			expected: "",
		},
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractBucketFromTUSMetadata(tt.header)
			assert.Equal(t, tt.expected, got)
		})
	}
}
