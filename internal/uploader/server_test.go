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

// injectClaims is a test helper that injects Claims into the request context.
func injectClaims(r *http.Request, c *Claims) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), claimsKey, c))
}

// MockS3Client satisfies the S3API interface for unit tests.
// All methods delegate to testify/mock for expectation tracking.
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

func TestNewTusHandler_ShouldCreateHandler_WhenValidBucketAndClientProvided(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)

	// Act
	h, err := NewTusHandler("test-bucket", mockS3)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, h)
}

func TestTusHandler_ShouldReturn201WithLocation_WhenUploadIsInitiated(t *testing.T) {
	// Arrange
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

	// Act
	mux.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Location"))
	mockS3.AssertExpectations(t)
}

func TestTusHandler_ShouldReturn500_WhenS3IsUnavailable(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	h, _ := NewTusHandler("test-bucket", mockS3)

	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3: ServiceUnavailable"))

	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	rr := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── ListFilesHandler ──────────────────────────────────────────────────────────

func TestListFilesHandler_ShouldReturn400_WhenBucketParamIsMissing(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	req, _ := http.NewRequest("GET", "/files/", nil)
	rr := httptest.NewRecorder()

	// Act
	app.ListFilesHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "bucket query parameter is required")
	mockS3.AssertNotCalled(t, "ListObjectsV2")
}

func TestListFilesHandler_ShouldReturnFilesWithoutSidecars_WhenBucketExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(input *s3.ListObjectsV2Input) bool {
		return aws.ToString(input.Bucket) == "my-videos"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("abc123"), Size: aws.Int64(1000)},
			{Key: aws.String("abc123.info"), Size: aws.Int64(100)},
		},
	}, nil)

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Bucket) == "my-videos" && aws.ToString(input.Key) == "abc123.info"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(`{"MetaData": {"filename": "movie.mp4"}}`)),
	}, nil)

	req, _ := http.NewRequest("GET", "/files/?bucket=my-videos", nil)
	rr := httptest.NewRecorder()

	// Act
	app.ListFilesHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "movie.mp4")
	assert.Contains(t, rr.Body.String(), "abc123")
	assert.Contains(t, rr.Body.String(), "/files/my-videos/abc123")
	assert.NotContains(t, rr.Body.String(), "abc123.info", ".info sidecar must not appear in the listing")
	mockS3.AssertExpectations(t)
}

func TestListFilesHandler_ShouldReturn404_WhenBucketNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})

	req, _ := http.NewRequest("GET", "/files/?bucket=ghost-bucket", nil)
	rr := httptest.NewRecorder()

	// Act
	app.ListFilesHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	mockS3.AssertExpectations(t)
}

// ── DownloadFileHandler ───────────────────────────────────────────────────────

func TestDownloadFileHandler_ShouldStreamFileWithOriginalName_WhenFileExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	fileContent := "hello world"

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "abc123.info"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(`{"MetaData": {"filename": "hello.txt"}}`)),
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

	// Act
	app.DownloadFileHandler(rr, req, "my-bucket", "abc123")

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, fileContent, rr.Body.String())
	assert.Contains(t, rr.Header().Get("Content-Disposition"), "hello.txt")
	assert.Equal(t, "text/plain", rr.Header().Get("Content-Type"))
	mockS3.AssertExpectations(t)
}

func TestDownloadFileHandler_ShouldReturn404_WhenFileNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "ghost.info"
	}), mock.Anything).Return(nil, &types.NoSuchKey{})

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return aws.ToString(input.Key) == "ghost"
	}), mock.Anything).Return(nil, &types.NoSuchKey{})

	req, _ := http.NewRequest("GET", "/files/my-bucket/ghost", nil)
	rr := httptest.NewRecorder()

	// Act
	app.DownloadFileHandler(rr, req, "my-bucket", "ghost")

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertExpectations(t)
}

// ── DeleteFileHandler ─────────────────────────────────────────────────────────

func TestDeleteFileHandler_ShouldDeleteFileAndSidecar_WhenFileExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

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

	// Act
	app.DeleteFileHandler(rr, req, "my-bucket", "abc123")

	// Assert
	assert.Equal(t, http.StatusNoContent, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestDeleteFileHandler_ShouldReturn404WithoutDeletion_WhenFileNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(input *s3.HeadObjectInput) bool {
		return aws.ToString(input.Key) == "ghost"
	}), mock.Anything).Return(nil, &types.NotFound{})

	req, _ := http.NewRequest("DELETE", "/files/my-bucket/ghost", nil)
	rr := httptest.NewRecorder()

	// Act
	app.DeleteFileHandler(rr, req, "my-bucket", "ghost")

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertNotCalled(t, "DeleteObjects")
	mockS3.AssertExpectations(t)
}

// ── ExtractBucketFromTUSMetadata ──────────────────────────────────────────────

func TestExtractBucketFromTUSMetadata_ShouldDecodeBucketName_WhenMetadataIsValid(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "bucket present alongside filename",
			header:   "filename dGVzdC50eHQ=,bucket bXktYnVja2V0",
			expected: "my-bucket",
		},
		{
			name:     "bucket is the only field",
			header:   "bucket bXktdmlkZW9z",
			expected: "my-videos",
		},
		{
			name:     "no bucket field present",
			header:   "filename dGVzdC50eHQ=",
			expected: "",
		},
		{
			name:     "empty header string",
			header:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange + Act
			got := ExtractBucketFromTUSMetadata(tt.header)

			// Assert
			assert.Equal(t, tt.expected, got)
		})
	}
}

// ── Access control ────────────────────────────────────────────────────────────

func TestListFilesHandler_ShouldReturn403_WhenBucketNotAllowed(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	req, _ := http.NewRequest("GET", "/files/?bucket=forbidden-bucket", nil)
	// User is only allowed "my-bucket"
	req = injectClaims(req, &Claims{AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.ListFilesHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "access denied")
	mockS3.AssertNotCalled(t, "ListObjectsV2")
}

func TestDownloadFileHandler_ShouldReturn403_WhenBucketNotAllowed(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	req, _ := http.NewRequest("GET", "/files/forbidden-bucket/key", nil)
	req = injectClaims(req, &Claims{AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.DownloadFileHandler(rr, req, "forbidden-bucket", "key")

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "access denied")
	mockS3.AssertNotCalled(t, "GetObject")
}

func TestDeleteFileHandler_ShouldReturn403_WhenBucketNotAllowed(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", S3Endpoint: "http://localhost:9000", Audit: &NoopAuditProducer{}}

	req, _ := http.NewRequest("DELETE", "/files/forbidden-bucket/key", nil)
	req = injectClaims(req, &Claims{AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	rr := httptest.NewRecorder()

	// Act
	app.DeleteFileHandler(rr, req, "forbidden-bucket", "key")

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "access denied")
	mockS3.AssertNotCalled(t, "HeadObject")
}
