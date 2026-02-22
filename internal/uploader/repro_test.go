package uploader

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestTusHandler_ShouldReturnLocationHeader_WhenUploadCreated(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	handler, _ := NewTusHandler("test-bucket", mockS3)

	mockS3.On("CreateMultipartUpload", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CreateMultipartUploadOutput{
		UploadId: aws.String("123"),
		Bucket:   aws.String("test-bucket"),
		Key:      aws.String("key"),
	}, nil)
	mockS3.On("PutObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, nil)

	req, _ := http.NewRequest("POST", "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Location"))
}

func TestTusHandler_ShouldReturnTusHeaders_WhenOptionsRequested(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	handler, _ := NewTusHandler("test-bucket", mockS3)

	req, _ := http.NewRequest("OPTIONS", "/files/", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Contains(t, rr.Header().Get("Tus-Resumable"), "1.0.0")
}

// TestTusHandler_ShouldReturn204_WhenChunkUploaded is skipped because tusd's
// s3store requires a pre-existing multipart upload state that cannot be fully
// reproduced with unit-level mocks. The equivalent scenario is covered by the
// integration test TestResumableUpload_E2E.
//
// func TestTusHandler_ShouldReturn204_WhenChunkUploaded(t *testing.T) { ... }
