package uploader

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)

// linkJSON returns a JSON representation of a shared link record.
func linkJSON(id, passwordHash, expiresAt string) string {
	return `{"id":"` + id + `","ownerUserId":"user1","bucket":"my-bucket","fileKey":"key.mp4","passwordHash":"` + passwordHash + `","expiresAt":"` + expiresAt + `","createdAt":"2025-01-01T00:00:00Z"}`
}

func newShareDownloadApp(sharesURL string, mockS3 *MockS3Client, mockPresigner *MockPresigner) *App {
	return &App{
		Shares:    NewSharesClient(sharesURL, ""),
		S3Client:  mockS3,
		Presigner: mockPresigner,
		Audit:     &NoopAuditProducer{},
	}
}

// ── GET /share/{id} ───────────────────────────────────────────────────────────

func TestShareDownloadHandler_ShouldReturn400_WhenIDMissing(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	r := httptest.NewRequest(http.MethodGet, "/share/", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestShareDownloadHandler_ShouldReturn503_WhenSharesNilOnGet(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	r := httptest.NewRequest(http.MethodGet, "/share/some-id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestShareDownloadHandler_ShouldReturn404_WhenLinkNotFound(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/share/missing-id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestShareDownloadHandler_ShouldReturn410_WhenLinkExpiredOnGet(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("exp-id", "", "2000-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/share/exp-id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusGone, w.Code)
}

func TestShareDownloadHandler_ShouldReturn200WithMeta_WhenLinkHasNoPassword(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, assert.AnError) // .info not found — falls back to fileKey

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("ok-id", "", "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, mockS3, new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/share/ok-id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, false, body["requiresPassword"])
	assert.NotEmpty(t, body["expiresAt"])
}

func TestShareDownloadHandler_ShouldReturn200WithRequiresPassword_WhenLinkHasPassword(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, assert.AnError)

	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("pw-id", string(hash), "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, mockS3, new(MockPresigner))
	r := httptest.NewRequest(http.MethodGet, "/share/pw-id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, true, body["requiresPassword"])
}

func TestShareDownloadHandler_ShouldReturn405_WhenMethodNotAllowed(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("id", "", "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodPut, "/share/id", nil)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// ── POST /share/{id} ──────────────────────────────────────────────────────────

func TestShareDownloadHandler_ShouldReturn503_WhenSharesNilOnPost(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	r := httptest.NewRequest(http.MethodPost, "/share/some-id", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestShareDownloadHandler_ShouldReturn410_WhenLinkExpiredOnPost(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("exp-id", "", "2000-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	body := bytes.NewBufferString(`{}`)
	r := httptest.NewRequest(http.MethodPost, "/share/exp-id", body)
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusGone, w.Code)
}

func TestShareDownloadHandler_ShouldReturn401_WhenPasswordRequiredButMissing(t *testing.T) {
	// Arrange
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("pw-id", string(hash), "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodPost, "/share/pw-id", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestShareDownloadHandler_ShouldReturn401_WhenPasswordIncorrect(t *testing.T) {
	// Arrange
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("pw-id", string(hash), "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := newShareDownloadApp(srv.URL, new(MockS3Client), new(MockPresigner))
	r := httptest.NewRequest(http.MethodPost, "/share/pw-id", bytes.NewBufferString(`{"password":"wrong"}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestShareDownloadHandler_ShouldReturn500_WhenPresignerNil(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("id", "", "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	app := &App{
		Shares:   NewSharesClient(srv.URL, ""),
		S3Client: new(MockS3Client),
		// Presigner: nil intentionally
		Audit: &NoopAuditProducer{},
	}
	r := httptest.NewRequest(http.MethodPost, "/share/id", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestShareDownloadHandler_ShouldReturn200WithDownloadURL_WhenPasswordCorrect(t *testing.T) {
	// Arrange
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("pw-id", string(hash), "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&v4.PresignedHTTPRequest{URL: "https://minio/my-bucket/key.mp4?token=xyz"}, nil)

	app := newShareDownloadApp(srv.URL, new(MockS3Client), mockPresigner)
	r := httptest.NewRequest(http.MethodPost, "/share/pw-id", bytes.NewBufferString(`{"password":"correct"}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Contains(t, body["downloadUrl"], "minio")
}

func TestShareDownloadHandler_ShouldReturn200WithDownloadURL_WhenNoPasswordRequired(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("id", "", "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&v4.PresignedHTTPRequest{URL: "https://minio/my-bucket/key.mp4?token=abc"}, nil)

	app := newShareDownloadApp(srv.URL, new(MockS3Client), mockPresigner)
	r := httptest.NewRequest(http.MethodPost, "/share/id", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.NotEmpty(t, body["downloadUrl"])
}

func TestShareDownloadHandler_ShouldReturn500_WhenPresignFails(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(linkJSON("id", "", "2099-01-01T00:00:00Z"))) //nolint:errcheck
	}))
	defer srv.Close()

	mockPresigner := new(MockPresigner)
	mockPresigner.On("PresignGetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, assert.AnError)

	app := newShareDownloadApp(srv.URL, new(MockS3Client), mockPresigner)
	r := httptest.NewRequest(http.MethodPost, "/share/id", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	// Act
	app.ShareDownloadHandler(w, r)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ── resolveFileName ───────────────────────────────────────────────────────────

func TestResolveFileName_ShouldReturnFilename_WhenInfoSidecarExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"my-video.mp4"}}`))}, nil)

	app := &App{S3Client: mockS3, Audit: &NoopAuditProducer{}}

	// Act
	name := app.resolveFileName(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "bucket", "file-key")

	// Assert
	assert.Equal(t, "my-video.mp4", name)
}

func TestResolveFileName_ShouldFallbackToFileKey_WhenInfoSidecarMissing(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, assert.AnError)

	app := &App{S3Client: mockS3, Audit: &NoopAuditProducer{}}

	// Act
	name := app.resolveFileName(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "bucket", "fallback-key")

	// Assert
	assert.Equal(t, "fallback-key", name)
}
