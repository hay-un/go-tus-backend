package uploader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func TestNewHandler_Creation(t *testing.T) {
	mockS3 := new(MockS3Client)
	// We only mock what's needed for initialization or checking existence if any
	
	handler, err := NewHandler("test-bucket", mockS3)
	assert.NoError(t, err)
	assert.NotNil(t, handler)
}

func TestTusCreation_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	handler, _ := NewHandler("test-bucket", mockS3)

	// s3store.NewUpload flow:
	// 1. CreateMultipartUpload to get UploadId
	// 2. PutObject to save .info file

	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CreateMultipartUploadOutput{
		UploadId: aws.String("upload-id-123"),
		Bucket:   aws.String("test-bucket"),
		Key:      aws.String("test-file"),
	}, nil)

	mockS3.On("PutObject", mock.Anything, mock.MatchedBy(func(input *s3.PutObjectInput) bool {
		return *input.Bucket == "test-bucket" && strings.HasSuffix(*input.Key, ".info")
	}), mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	// Perform Request
	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	req.Header.Set("Upload-Metadata", "filename dGVzdC50eHQ=")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Assertions
	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Location"))
	mockS3.AssertExpectations(t)
}

func TestTusCreation_StorageFailure(t *testing.T) {
	mockS3 := new(MockS3Client)
	handler, _ := NewHandler("test-bucket", mockS3)

	// Simulate S3 Error during Multipart creation (e.g. Storage Full/Permissions)
	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("s3: ServiceUnavailable"))

	// We might also need to expect PutObject if it happens before, but usually NewUpload does Multipart first or parallel.
	// If PutObject is called first, we should allow it or fail it. 
	// Let's assume CreateMultipartUpload is the failure point.

	// Perform Request
	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Assertions
	// Tusd usually returns 500 for storage errors
	assert.NotEqual(t, http.StatusCreated, rr.Code)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}
