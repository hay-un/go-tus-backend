package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/mock"
)

// ── ProcessPurgeEvent ─────────────────────────────────────────────────────────

func TestProcessPurgeEvent_ShouldPurgeBucket_WhenAllSucceeds(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	sharesCallCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sharesCallCount++
		w.WriteHeader(http.StatusNoContent)
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"data": nil}) //nolint:errcheck
		}
	}))
	defer srv.Close()

	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Content:  &NoopContentProducer{},
		Shares:   NewSharesClient(srv.URL, "test-secret"),
	}

	mockS3.On("ListObjectsV2", mock.Anything, mock.MatchedBy(func(in *s3.ListObjectsV2Input) bool {
		return aws.ToString(in.Bucket) == "user-files"
	}), mock.Anything).Return(&s3.ListObjectsV2Output{
		Contents: []types.Object{{Key: aws.String("photo.jpg")}},
	}, nil)

	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteObjectsOutput{}, nil)

	mockS3.On("DeleteBucket", mock.Anything, mock.MatchedBy(func(in *s3.DeleteBucketInput) bool {
		return aws.ToString(in.Bucket) == "user-files"
	}), mock.Anything).Return(&s3.DeleteBucketOutput{}, nil)

	event, _ := json.Marshal(map[string]string{"bucketName": "user-files", "ownerUserId": "user-uuid"})

	// Act
	app.ProcessPurgeEvent(context.Background(), event)

	// Assert
	mockS3.AssertExpectations(t)
}

func TestProcessPurgeEvent_ShouldReturn_WhenInvalidJSON(t *testing.T) {
	// Arrange — invalid JSON should be silently skipped (logged)
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	// Act — should not panic
	app.ProcessPurgeEvent(context.Background(), []byte(`{bad json`))
}

func TestProcessPurgeEvent_ShouldReturn_WhenBucketNameEmpty(t *testing.T) {
	// Arrange
	app := &App{Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}
	event, _ := json.Marshal(map[string]string{"bucketName": "", "ownerUserId": "user-uuid"})

	// Act — should not panic
	app.ProcessPurgeEvent(context.Background(), event)
}

func TestProcessPurgeEvent_ShouldReturn_WhenListObjectsFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("connection error"))

	event, _ := json.Marshal(map[string]string{"bucketName": "user-files"})

	// Act
	app.ProcessPurgeEvent(context.Background(), event)

	// Assert — function returns early on list error
	mockS3.AssertExpectations(t)
}

func TestProcessPurgeEvent_ShouldReturn_WhenDeleteObjectsFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{
			Contents: []types.Object{{Key: aws.String("file.txt")}},
		}, nil)
	mockS3.On("DeleteObjects", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("delete error"))

	event, _ := json.Marshal(map[string]string{"bucketName": "user-files"})

	// Act
	app.ProcessPurgeEvent(context.Background(), event)

	// Assert — function returns early on delete error
	mockS3.AssertExpectations(t)
}

func TestProcessPurgeEvent_ShouldSkipObjectDelete_WhenBucketEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	app := &App{
		S3Client: mockS3,
		Audit:    &NoopAuditProducer{},
		Content:  &NoopContentProducer{},
		Shares:   NewSharesClient(srv.URL, "test-secret"),
	}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: []types.Object{}}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	event, _ := json.Marshal(map[string]string{"bucketName": "empty-bucket"})

	// Act
	app.ProcessPurgeEvent(context.Background(), event)

	// Assert — DeleteObjects should NOT be called for empty bucket
	mockS3.AssertNumberOfCalls(t, "DeleteObjects", 0)
	mockS3.AssertExpectations(t)
}

func TestProcessPurgeEvent_ShouldContinue_WhenSharesNil(t *testing.T) {
	// Arrange — Shares is nil; should skip share cleanup without panic
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}, Shares: nil}

	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{Contents: []types.Object{}}, nil)
	mockS3.On("DeleteBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.DeleteBucketOutput{}, nil)

	event, _ := json.Marshal(map[string]string{"bucketName": "user-files", "ownerUserId": "uuid"})

	// Act
	app.ProcessPurgeEvent(context.Background(), event)

	// Assert
	mockS3.AssertExpectations(t)
}
