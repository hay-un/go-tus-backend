package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockPresigner satisfies the Presigner interface for unit tests.
type MockPresigner struct {
	mock.Mock
}

func (m *MockPresigner) PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*v4.PresignedHTTPRequest), args.Error(1)
}

func newShareApp(mockS3 *MockS3Client, mockPresigner *MockPresigner) *App {
	return &App{
		S3Client:  mockS3,
		Presigner: mockPresigner,
		Audit:     &NoopAuditProducer{},
	}
}

func TestShareFileHandler_ShouldReturn400_WhenBucketMissing(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?file=my-key", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn400_WhenFileMissing(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn403_WhenAccessDenied(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=other-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestShareFileHandler_ShouldReturn400_WhenExpiryInvalid(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key&expiry=notaduration", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn400_WhenExpiryIsNegative(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key&expiry=-1h", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn404_WhenFileNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NotFound{})

	app := newShareApp(mockS3, new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=missing-key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestShareFileHandler_ShouldReturn500_WhenPresignFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("presign error"))

	app := newShareApp(mockS3, mockPresigner)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestShareFileHandler_ShouldReturn200WithURL_WhenSuccessful(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&v4.PresignedHTTPRequest{URL: "https://minio/my-bucket/key?X-Amz-Signature=abc"}, nil)

	app := newShareApp(mockS3, mockPresigner)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key&expiry=24h", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Contains(t, body["url"], "X-Amz-Signature")
	assert.NotEmpty(t, body["expiresAt"])
}

func TestShareFileHandler_ShouldUseDefaultExpiry_WhenExpiryParamAbsent(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&v4.PresignedHTTPRequest{URL: "https://minio/bucket/key?token=x"}, nil)

	app := newShareApp(mockS3, mockPresigner)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	mockPresigner.AssertCalled(t, "PresignGetObject", mock.Anything, mock.Anything, mock.Anything)
}

func TestShareFileHandler_ShouldAllowAccess_WhenAdminClaims(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&v4.PresignedHTTPRequest{URL: "https://minio/any/key"}, nil)

	app := newShareApp(mockS3, mockPresigner)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=any-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "admin1", AllowedBuckets: []string{"*"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
}
