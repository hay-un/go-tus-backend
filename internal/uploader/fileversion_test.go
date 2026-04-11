package uploader

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/stretchr/testify/require"
)

// ── ArchiveFileVersionHandler ─────────────────────────────────────────────────

func TestArchiveFileVersionHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{
		S3Client:        mockS3,
		Audit:           &NoopAuditProducer{},
		Shares:          nil,
		MaxFileVersions: 5,
	}
	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{ContentLength: aws.Int64(1000)}, nil)

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/file-uuid/version", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ArchiveFileVersionHandler(w, r, "my-bucket", "file-uuid")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestArchiveFileVersionHandler_ShouldReturn403_WhenAccessDenied(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{
		S3Client:        mockS3,
		Audit:           &NoopAuditProducer{},
		Shares:          nil,
		MaxFileVersions: 5,
	}

	r := httptest.NewRequest(http.MethodPost, "/files/other-bucket/file-uuid/version", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ArchiveFileVersionHandler(w, r, "other-bucket", "file-uuid")

	// Assert
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestArchiveFileVersionHandler_ShouldReturn201_WhenSuccessful(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)

	archivedAt := time.Now().UTC()
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "file-versions"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(FileVersionRecord{ //nolint:errcheck
				ID:         "ver-1",
				Bucket:     "my-bucket",
				Filename:   "report.pdf",
				S3Key:      "file-uuid",
				VersionNum: 1,
				Size:       1024,
				ArchivedAt: archivedAt,
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "count"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]int{"count": 1}) //nolint:errcheck
		}
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client:        mockS3,
		Audit:           &NoopAuditProducer{},
		Shares:          NewSharesClient(sharesServer.URL, "test-secret"),
		MaxFileVersions: 5,
	}

	// HeadObject to get size
	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(in *s3.HeadObjectInput) bool {
		return aws.ToString(in.Bucket) == "my-bucket" && aws.ToString(in.Key) == "file-uuid"
	}), mock.Anything).Return(&s3.HeadObjectOutput{ContentLength: aws.Int64(1024)}, nil)

	// GetObject for .info sidecar (resolveFileName)
	infoBody := `{"MetaData":{"filename":"report.pdf"}}`
	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(in *s3.GetObjectInput) bool {
		return strings.HasSuffix(aws.ToString(in.Key), ".info")
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(infoBody)),
	}, nil)

	// CopyObject: main file to __versions__/
	mockS3.On("CopyObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CopyObjectOutput{}, nil)

	// DeleteObjects: remove originals
	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/file-uuid/version", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ArchiveFileVersionHandler(w, r, "my-bucket", "file-uuid")

	// Assert
	assert.Equal(t, http.StatusCreated, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "ver-1", body["id"])
	assert.Equal(t, float64(1), body["versionNum"])
}

func TestArchiveFileVersionHandler_ShouldAutoDeleteOldest_WhenMaxVersionsExceeded(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)

	deletedVersionID := ""
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/file-versions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(FileVersionRecord{ //nolint:errcheck
				ID: "ver-new", VersionNum: 6, ArchivedAt: time.Now().UTC(),
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/count"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]int{"count": 6}) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/oldest"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(FileVersionRecord{ //nolint:errcheck
				ID:    "ver-oldest",
				S3Key: "old-uuid",
			})
		case r.Method == http.MethodDelete:
			deletedVersionID = strings.TrimPrefix(r.URL.Path, "/internal/file-versions/")
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client:        mockS3,
		Audit:           &NoopAuditProducer{},
		Shares:          NewSharesClient(sharesServer.URL, "test-secret"),
		MaxFileVersions: 5,
	}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{ContentLength: aws.Int64(512)}, nil)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"doc.pdf"}}`)),
		}, nil)
	mockS3.On("CopyObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CopyObjectOutput{}, nil)
	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/file-uuid/version", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ArchiveFileVersionHandler(w, r, "my-bucket", "file-uuid")

	// Assert
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "ver-oldest", deletedVersionID)
}

// ── ListFileVersionsHandler ───────────────────────────────────────────────────

func TestListFileVersionsHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Shares: nil}

	r := httptest.NewRequest(http.MethodGet, "/files/my-bucket/versions?filename=report.pdf", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ListFileVersionsHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestListFileVersionsHandler_ShouldReturn400_WhenFilenameMissing(t *testing.T) {
	// Arrange
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []FileVersionRecord{}}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}

	r := httptest.NewRequest(http.MethodGet, "/files/my-bucket/versions", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ListFileVersionsHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListFileVersionsHandler_ShouldReturn200_WithVersionList(t *testing.T) {
	// Arrange
	var capturedQuery string
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []FileVersionRecord{
				{ID: "ver-1", Filename: "report.pdf", VersionNum: 1},
				{ID: "ver-2", Filename: "report.pdf", VersionNum: 2},
			},
		})
	}))
	defer sharesServer.Close()

	app := &App{
		Audit:  &NoopAuditProducer{},
		Shares: NewSharesClient(sharesServer.URL, "test-secret"),
	}

	r := httptest.NewRequest(http.MethodGet, "/files/my-bucket/versions?filename=report.pdf", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.ListFileVersionsHandler(w, r, "my-bucket")

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedQuery, "filename=report.pdf")
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	data := body["data"].([]interface{})
	assert.Len(t, data, 2)
}

// ── RestoreFileVersionHandler ─────────────────────────────────────────────────

func TestRestoreFileVersionHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   nil,
	}

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/versions/ver-1/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.RestoreFileVersionHandler(w, r, "my-bucket", "ver-1")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRestoreFileVersionHandler_ShouldReturn404_WhenVersionNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ListFileVersions returns empty list for any filename
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []FileVersionRecord{}}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   NewSharesClient(sharesServer.URL, "test-secret"),
	}

	// ListObjectsV2 returns one .info file in __versions__/
	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Prefix) == versionsPrefix
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("__versions__/old-uuid.info")},
		},
	}, nil)

	// GetObject for .info sidecar
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"report.pdf"}}`)),
		}, nil)

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/versions/no-such/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.RestoreFileVersionHandler(w, r, "my-bucket", "no-such")

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRestoreFileVersionHandler_ShouldReturn200_WhenSuccessful(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)

	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "file-versions") && !strings.Contains(r.URL.Path, "/count") && !strings.Contains(r.URL.Path, "/oldest"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"data": []FileVersionRecord{
					{ID: "ver-1", Bucket: "my-bucket", Filename: "report.pdf", S3Key: "old-uuid", VersionNum: 1},
				},
			})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []FileVersionRecord{}}) //nolint:errcheck
		}
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   NewSharesClient(sharesServer.URL, "test-secret"),
	}

	// ListObjectsV2 for __versions__/ prefix
	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Prefix) == versionsPrefix
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("__versions__/old-uuid.info")},
		},
	}, nil)

	// ListObjectsV2 for active files (no prefix)
	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return in.Prefix == nil || aws.ToString(in.Prefix) == ""
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{},
	}, nil)

	// GetObject for .info sidecar reads
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"report.pdf"}}`)),
		}, nil)

	// CopyObject: restore from __versions__/ to original
	mockS3.On("CopyObject", mock.Anything, mock.Anything, mock.Anything).Return(&s3.CopyObjectOutput{}, nil)

	// DeleteObjects: delete archived copies
	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	r := httptest.NewRequest(http.MethodPost, "/files/my-bucket/versions/ver-1/restore", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.RestoreFileVersionHandler(w, r, "my-bucket", "ver-1")

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "old-uuid", body["restoredKey"])
}

// ── DeleteFileVersionHandler ──────────────────────────────────────────────────

func TestDeleteFileVersionHandler_ShouldReturn503_WhenSharesNil(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   nil,
	}

	r := httptest.NewRequest(http.MethodDelete, "/files/my-bucket/versions/ver-1", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteFileVersionHandler(w, r, "my-bucket", "ver-1")

	// Assert
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestDeleteFileVersionHandler_ShouldReturn404_WhenVersionNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []FileVersionRecord{}}) //nolint:errcheck
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   NewSharesClient(sharesServer.URL, "test-secret"),
	}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("__versions__/old-uuid.info")},
		},
	}, nil)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"report.pdf"}}`)),
		}, nil)

	r := httptest.NewRequest(http.MethodDelete, "/files/my-bucket/versions/no-such", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteFileVersionHandler(w, r, "my-bucket", "no-such")

	// Assert
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteFileVersionHandler_ShouldReturn204_WhenSuccessful(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	sharesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
				"data": []FileVersionRecord{
					{ID: "ver-1", S3Key: "old-uuid", Filename: "report.pdf"},
				},
			})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer sharesServer.Close()

	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Shares:   NewSharesClient(sharesServer.URL, "test-secret"),
	}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{
			{Key: aws.String("__versions__/old-uuid.info")},
		},
	}, nil)
	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader(`{"MetaData":{"filename":"report.pdf"}}`)),
		}, nil)
	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).Return(&s3.DeleteObjectsOutput{}, nil)

	r := httptest.NewRequest(http.MethodDelete, "/files/my-bucket/versions/ver-1", nil)
	r = injectClaims(r, &Claims{Subject: "user-1", AllowedBuckets: []string{"my-bucket"}})
	w := httptest.NewRecorder()

	// Act
	app.DeleteFileVersionHandler(w, r, "my-bucket", "ver-1")

	// Assert
	assert.Equal(t, http.StatusNoContent, w.Code)
}

// ── SharesClient FileVersion methods ─────────────────────────────────────────

func TestSharesClientCreateFileVersion_ShouldReturn201_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/internal/file-versions", r.URL.Path)
		assert.Equal(t, "test-secret", r.Header.Get("X-Internal-Secret"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(FileVersionRecord{ //nolint:errcheck
			ID: "ver-1", Bucket: "my-bucket", Filename: "report.pdf", S3Key: "uuid-1", VersionNum: 1,
			Size: 2048, ArchivedAt: time.Now().UTC(),
		})
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	rec, err := client.CreateFileVersion(context.Background(), "my-bucket", "report.pdf", "uuid-1", 2048)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "ver-1", rec.ID)
	assert.Equal(t, 1, rec.VersionNum)
}

func TestSharesClientCreateFileVersion_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, err := client.CreateFileVersion(context.Background(), "my-bucket", "report.pdf", "uuid-1", 2048)

	// Assert
	require.Error(t, err)
}

func TestSharesClientListFileVersions_ShouldReturnVersions_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.RawQuery, "bucket=my-bucket")
		assert.Contains(t, r.URL.RawQuery, "filename=report.pdf")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"data": []FileVersionRecord{
				{ID: "ver-1", Filename: "report.pdf", VersionNum: 1},
			},
		})
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	versions, err := client.ListFileVersions(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.NoError(t, err)
	assert.Len(t, versions, 1)
	assert.Equal(t, "ver-1", versions[0].ID)
}

func TestSharesClientListFileVersions_ShouldReturnEmpty_WhenDataNull(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": nil}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	versions, err := client.ListFileVersions(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.NoError(t, err)
	assert.Empty(t, versions)
}

func TestSharesClientGetOldestFileVersion_ShouldReturnRecord_WhenFound(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "oldest")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(FileVersionRecord{ID: "ver-oldest", VersionNum: 1}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	rec, err := client.GetOldestFileVersion(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "ver-oldest", rec.ID)
}

func TestSharesClientGetOldestFileVersion_ShouldReturnNil_WhenNotFound(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	rec, err := client.GetOldestFileVersion(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.NoError(t, err)
	assert.Nil(t, rec)
}

func TestSharesClientCountFileVersions_ShouldReturnCount_WhenSuccessful(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "count")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"count": 3}) //nolint:errcheck
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	count, err := client.CountFileVersions(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestSharesClientCountFileVersions_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	_, err := client.CountFileVersions(context.Background(), "my-bucket", "report.pdf")

	// Assert
	require.Error(t, err)
}

func TestSharesClientDeleteFileVersion_ShouldSucceed_WhenRecordExists(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Contains(t, r.URL.Path, "ver-1")
		w.WriteHeader(http.StatusNoContent)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteFileVersion(context.Background(), "ver-1")

	// Assert
	require.NoError(t, err)
}

func TestSharesClientDeleteFileVersion_ShouldError_WhenServerFails(t *testing.T) {
	// Arrange
	srv := newMockSharesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := NewSharesClient(srv.URL, "test-secret")

	// Act
	err := client.DeleteFileVersion(context.Background(), "ver-1")

	// Assert
	require.Error(t, err)
}
