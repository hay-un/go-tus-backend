package uploader

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ── parseRSAPublicKeyFromJWK ──────────────────────────────────────────────────

func b64urlEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func expB64(e int) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(e))
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b64urlEncode(b)
}

func TestParseRSAPublicKeyFromJWK_ShouldReturnKey_WhenValidInputs(t *testing.T) {
	// Arrange — generate a real RSA key and use its fields
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NoError(t, err)
	nB64 := b64urlEncode(new(big.Int).SetBytes(key.PublicKey.N.Bytes()).Bytes())
	eB64 := expB64(key.PublicKey.E)

	// Act
	pub, err := parseRSAPublicKeyFromJWK(nB64, eB64)

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, key.PublicKey.N, pub.N)
	assert.Equal(t, key.PublicKey.E, pub.E)
}

func TestParseRSAPublicKeyFromJWK_ShouldFail_WhenModulusInvalid(t *testing.T) {
	_, err := parseRSAPublicKeyFromJWK("!!!not-base64url", expB64(65537))
	assert.ErrorContains(t, err, "decode modulus")
}

func TestParseRSAPublicKeyFromJWK_ShouldFail_WhenExponentInvalid(t *testing.T) {
	_, err := parseRSAPublicKeyFromJWK(b64urlEncode([]byte{1, 2, 3}), "!!!not-base64url")
	assert.ErrorContains(t, err, "decode exponent")
}

func TestParseRSAPublicKeyFromJWK_ShouldFail_WhenExponentIsZero(t *testing.T) {
	// A single zero byte decodes to exponent 0
	_, err := parseRSAPublicKeyFromJWK(b64urlEncode([]byte{1, 2, 3}), b64urlEncode([]byte{0}))
	assert.ErrorContains(t, err, "invalid exponent")
}

// ── BucketItemHandler routing ─────────────────────────────────────────────────

func TestBucketItemHandler_ShouldReturn503_WhenGetTrash(t *testing.T) {
	// Arrange — Shares=nil → ListBucketTrashHandler returns 503
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodGet, "/buckets/trash", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com"})
	w := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(w, req)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestBucketItemHandler_ShouldReturn405_WhenPostToTrash(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodPost, "/buckets/trash", nil)
	w := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(w, req)

	// Assert
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestBucketItemHandler_ShouldRoute_WhenRenamePath(t *testing.T) {
	// Arrange — rename with no body → 400 (bad JSON), but routing is covered
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/rename", nil)
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(w, req)

	// Assert — routing dispatches to renameBucketHandler which returns 400 for bad body
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBucketItemHandler_ShouldReturn405_WhenGetToRenamePath(t *testing.T) {
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/rename", nil)
	w := httptest.NewRecorder()
	app.BucketItemHandler(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestBucketItemHandler_ShouldReturn503_WhenPostToRestorePath(t *testing.T) {
	// Arrange — Shares=nil → RestoreBucketHandler returns 503
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodPost, "/buckets/my-bucket/restore", nil)
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(w, req)

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestBucketItemHandler_ShouldReturn405_WhenGetToRestorePath(t *testing.T) {
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/restore", nil)
	w := httptest.NewRecorder()
	app.BucketItemHandler(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestBucketItemHandler_ShouldRouteToSharesHandler_WhenSharesPath(t *testing.T) {
	// Arrange — Shares=nil → SharesItemHandler returns 503
	app := &App{Audit: &NoopAuditProducer{}}
	req := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket/shares", nil)
	req = injectClaims(req, &Claims{Subject: "u", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.BucketItemHandler(w, req)

	// Assert — SharesItemHandler returns 503 when Shares=nil
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestBucketItemHandler_ShouldReturn405_WhenDefaultMethod(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)
	req := httptest.NewRequest(http.MethodGet, "/buckets/my-bucket", nil)
	w := httptest.NewRecorder()
	app.BucketItemHandler(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// ── FilesHandler routing ──────────────────────────────────────────────────────

// newFilesApp builds a minimal App suitable for FilesHandler routing tests.
// The S3 mock is set up to handle any request needed by the underlying handlers.
func newFilesApp(mockS3 *MockS3Client) *App {
	return &App{
		S3Client:   mockS3,
		BucketName: "default-bucket",
		S3Endpoint: "http://localhost:9000",
		Audit:      &NoopAuditProducer{},
		// TusHandler left nil — only tested for non-TUS routes here
	}
}

func TestFilesHandler_ShouldRouteToListFiles_WhenGetFilesRoot(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodGet, "/files/", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// ListFilesHandler returns 400 when bucket param is missing
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFilesHandler_ShouldRouteToShareFile_WhenGetFilesShare(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodGet, "/files/share", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// ShareFileHandler returns 400 when bucket/file params missing
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFilesHandler_ShouldRouteToListSharedLinks_WhenGetSharedLinks(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3) // Shares=nil → 503

	req := httptest.NewRequest(http.MethodGet, "/files/shared-links", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestFilesHandler_ShouldRouteToDeleteSharedLink_WhenDeleteSharedLinks(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3) // Shares=nil → 503

	req := httptest.NewRequest(http.MethodDelete, "/files/shared-links/some-link-id", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestFilesHandler_ShouldRouteToListFileTrash_WhenGetBucketTrash(t *testing.T) {
	mockS3 := new(MockS3Client)
	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: []types.Object{}}, nil)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodGet, "/files/my-bucket/trash", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// ListFileTrashHandler returns 200 with empty data
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestFilesHandler_ShouldRouteToDownloadFile_WhenGetBucketKey(t *testing.T) {
	mockS3 := new(MockS3Client)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("not found"))
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodGet, "/files/my-bucket/photo.jpg", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// DownloadFileHandler — S3 error → 500 or 404
	assert.True(t, w.Code >= 400)
}

func TestFilesHandler_ShouldRouteToDeleteFile_WhenDeleteBucketKey(t *testing.T) {
	mockS3 := new(MockS3Client)
	// DeleteFileHandler first does HeadObject to check for conflicts; mock it as not found
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchKey{})
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("not found"))
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodDelete, "/files/my-bucket/photo.jpg", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	assert.True(t, w.Code >= 400)
}

func TestFilesHandler_ShouldReturn405_WhenPostBucketKeyNotRestore(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodPost, "/files/my-bucket/photo.jpg", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestFilesHandler_ShouldReturn405_WhenPutBucketKey(t *testing.T) {
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodPut, "/files/my-bucket/photo.jpg", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestFilesHandler_ShouldRouteToStreamFile_WhenGetBucketKeyStream(t *testing.T) {
	mockS3 := new(MockS3Client)
	// StreamFileHandler first calls HeadObject
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("not found"))
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodGet, "/files/my-bucket/video.mp4/stream", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// StreamFileHandler returns >= 400 when S3 fails
	assert.True(t, w.Code >= 400)
}

func TestFilesHandler_ShouldRouteToRestoreFile_WhenPostBucketTrashKeyRestore(t *testing.T) {
	mockS3 := new(MockS3Client)
	// RestoreFileHandler calls HeadObject to check destination, then GetObject for trash copy
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchKey{})
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("not found"))
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodPost, "/files/my-bucket/trash/abc123/restore", nil)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	app.FilesHandler(w, req)

	// RestoreFileHandler returns >= 400 when object not found
	assert.True(t, w.Code >= 400)
}

// ── TUS routing in FilesHandler ───────────────────────────────────────────────

func TestFilesHandler_ShouldReturn400_WhenTUSPostWithMissingBucket(t *testing.T) {
	// Arrange — TUS POST with Upload-Metadata that has no "bucket" key
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	// No Upload-Metadata or empty bucket value
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"*"}, Role: "admin"})
	w := httptest.NewRecorder()

	// Act
	app.FilesHandler(w, req)

	// Assert — returns 400 because bucket is empty
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFilesHandler_ShouldReturn403_WhenTUSPostAccessDenied(t *testing.T) {
	// Arrange — TUS POST with bucket the user does not own
	mockS3 := new(MockS3Client)
	app := newFilesApp(mockS3)

	// Encode "my-bucket" as base64 for Upload-Metadata
	encoded := base64.StdEncoding.EncodeToString([]byte("other-bucket"))
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	req.Header.Set("Upload-Metadata", "bucket "+encoded)
	req = injectClaims(req, &Claims{Subject: "u", Email: "u@test.com", AllowedBuckets: []string{"my-bucket"}, Role: "user"})
	w := httptest.NewRecorder()

	// Act
	app.FilesHandler(w, req)

	// Assert — returns 403 because "other-bucket" is not in AllowedBuckets
	assert.Equal(t, http.StatusForbidden, w.Code)
}
