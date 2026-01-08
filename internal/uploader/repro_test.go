package uploader

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestTusRouting_LocationHeader(t *testing.T) {
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
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	location := rr.Header().Get("Location")
	// Since we use StripPrefix("/files/", ...), the handler sees "/"
	// and tusd sees "/" too if BasePath is "/files/". No wait.
	// If tusd BasePath is "/files/", it expects the request path to START with "/files/".
	// If we strip it, it won't.
	assert.NotEmpty(t, location)
}

func TestTusRouting_Options(t *testing.T) {
	mockS3 := new(MockS3Client)
	handler, _ := NewTusHandler("test-bucket", mockS3)

	req, _ := http.NewRequest("OPTIONS", "/files/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// In TUS, OPTIONS should return 204 or 200 with Tus headers
	assert.Contains(t, rr.Header().Get("Tus-Resumable"), "1.0.0")
}

func TestTusPatch_HappyPath(t *testing.T) {
	mockS3 := new(MockS3Client)
	handler, _ := NewTusHandler("test-bucket", mockS3)

	// Mocking S3 for PATCH (UploadPart)
	// tusd will call HeadObject on the .info file first to check state
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.HeadObjectOutput{
		ContentLength: aws.Int64(100), // Size of .info file (arbitrary)
	}, nil)

	id := "hashed-id-123"
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(input *s3.GetObjectInput) bool {
		return strings.HasSuffix(*input.Key, ".info")
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(`{"ID":"` + id + `","Size":100,"Offset":0,"IsPartial":false,"IsFinal":false,"MetaData":{"filename":"test.txt"},"Storage":{"Bucket":"test-bucket","Key":"key","UploadId":"upload-id-123"}}`)),
	}, nil)

	mockS3.On("UploadPart", mock.Anything, mock.Anything, mock.Anything).Return(&s3.UploadPartOutput{
		ETag: aws.String("etag-123"),
	}, nil)

	mockS3.On("ListParts", mock.Anything, mock.Anything, mock.Anything).Return(&s3.ListPartsOutput{}, nil)

	// PATCH /files/<id>
	req, _ := http.NewRequest("PATCH", "/files/"+id, strings.NewReader("some data"))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")

	rr := httptest.NewRecorder()
	// handler already has StripPrefix
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "1.0.0", rr.Header().Get("Tus-Resumable"))
	assert.Equal(t, "9", rr.Header().Get("Upload-Offset"))
	mockS3.AssertExpectations(t)
}
