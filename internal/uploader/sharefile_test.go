package uploader

import (
	"context"
	"encoding/json"
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

const testSharedLinkJSON = `{"id":"test-share-id","ownerUserId":"user1","bucket":"my-bucket","fileKey":"key","passwordHash":"","expiresAt":"2099-01-01T00:00:00Z","createdAt":"2025-01-01T00:00:00Z"}`

func newShareApp(mockS3 *MockS3Client) *App {
	return &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
	}
}

func newShareAppWithShares(mockS3 *MockS3Client, sharesURL string) *App {
	return &App{
		S3Client: mockS3,
		Shares:   NewSharesClient(sharesURL, ""),
		Audit:    &NoopAuditProducer{},
		Content:  &NoopContentProducer{},
	}
}

func TestShareFileHandler_ShouldReturn400_WhenBucketMissing(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client))
	r := httptest.NewRequest(http.MethodGet, "/files/share?file=my-key", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn400_WhenFileMissing(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client))
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareFileHandler_ShouldReturn403_WhenAccessDenied(t *testing.T) {
	// Arrange
	app := newShareApp(new(MockS3Client))
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
	app := newShareApp(new(MockS3Client))
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
	app := newShareApp(new(MockS3Client))
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

	app := newShareApp(mockS3)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=missing-key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestShareFileHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	app := newShareApp(mockS3) // Shares == nil
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestShareFileHandler_ShouldReturn500_WhenSharesClientFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	app := newShareAppWithShares(mockS3, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestShareFileHandler_ShouldReturn200WithShareId_WhenSuccessful(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(testSharedLinkJSON)) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareAppWithShares(mockS3, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key&expiry=24h", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "test-share-id", body["shareId"])
	assert.NotEmpty(t, body["expiresAt"])
}

func TestShareFileHandler_ShouldHashPassword_WhenPasswordProvided(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	var capturedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody) //nolint:errcheck
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(testSharedLinkJSON)) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareAppWithShares(mockS3, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key&password=secret", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, capturedBody["passwordHash"])
	assert.NotEqual(t, "secret", capturedBody["passwordHash"])
}

func TestShareFileHandler_ShouldUseDefaultExpiry_WhenExpiryParamAbsent(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(testSharedLinkJSON)) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareAppWithShares(mockS3, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=my-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "user1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.NotEmpty(t, body["shareId"])
}

func TestShareFileHandler_ShouldAllowAccess_WhenAdminClaims(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{}, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"admin-share-id","ownerUserId":"admin1","bucket":"any-bucket","fileKey":"key","passwordHash":"","expiresAt":"2099-01-01T00:00:00Z","createdAt":"2025-01-01T00:00:00Z"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareAppWithShares(mockS3, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/files/share?bucket=any-bucket&file=key", nil)
	r = injectClaims(r, &Claims{Subject: "admin1", AllowedBuckets: []string{"*"}})
	w := httptest.NewRecorder()

	// Act
	app.ShareFileHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
}
