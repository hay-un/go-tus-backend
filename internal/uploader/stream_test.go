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

// ── StreamFileHandler ─────────────────────────────────────────────────────────

func TestStreamFileHandler_ShouldReturn200WithFullBody_WhenNoRangeHeader(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	fileContent := "fake video bytes"
	totalSize := int64(len(fileContent))

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(in *s3.HeadObjectInput) bool {
		return aws.ToString(in.Bucket) == "my-videos" && aws.ToString(in.Key) == "abc123"
	}), mock.Anything).Return(&s3.HeadObjectOutput{
		ContentLength: aws.Int64(totalSize),
		ContentType:   aws.String("video/mp4"),
	}, nil)

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(in *s3.GetObjectInput) bool {
		return aws.ToString(in.Bucket) == "my-videos" &&
			aws.ToString(in.Key) == "abc123" &&
			in.Range == nil
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(fileContent)),
		ContentLength: aws.Int64(totalSize),
	}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))
	assert.Equal(t, "bytes", rr.Header().Get("Accept-Ranges"))
	assert.Equal(t, "inline", rr.Header().Get("Content-Disposition"))
	assert.Equal(t, fileContent, rr.Body.String())
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn206WithPartialBody_WhenRangeHeaderPresent(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	fullContent := "0123456789abcdefghijklmnopqrstuvwxyz" // 36 bytes
	partialContent := fullContent[0:10]                   // bytes 0-9

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(in *s3.HeadObjectInput) bool {
		return aws.ToString(in.Bucket) == "my-videos" && aws.ToString(in.Key) == "abc123"
	}), mock.Anything).Return(&s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(fullContent))),
		ContentType:   aws.String("video/mp4"),
	}, nil)

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(in *s3.GetObjectInput) bool {
		return aws.ToString(in.Bucket) == "my-videos" &&
			aws.ToString(in.Key) == "abc123" &&
			aws.ToString(in.Range) == "bytes=0-9"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(partialContent)),
		ContentLength: aws.Int64(10),
	}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	req.Header.Set("Range", "bytes=0-9")
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusPartialContent, rr.Code)
	assert.Equal(t, "bytes 0-9/36", rr.Header().Get("Content-Range"))
	assert.Equal(t, "10", rr.Header().Get("Content-Length"))
	assert.Equal(t, "bytes", rr.Header().Get("Accept-Ranges"))
	assert.Equal(t, partialContent, rr.Body.String())
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn206WithSuffixRange_WhenRangeIsNegative(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	partialContent := "6789" // last 4 bytes of "0123456789"

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{
			ContentLength: aws.Int64(10),
			ContentType:   aws.String("video/mp4"),
		}, nil)

	mockS3.On("GetObject", mock.Anything, mock.MatchedBy(func(in *s3.GetObjectInput) bool {
		return aws.ToString(in.Range) == "bytes=6-9"
	}), mock.Anything).Return(&s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(partialContent)),
	}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	req.Header.Set("Range", "bytes=-4")
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusPartialContent, rr.Code)
	assert.Equal(t, "bytes 6-9/10", rr.Header().Get("Content-Range"))
	assert.Equal(t, partialContent, rr.Body.String())
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn416_WhenRangeExceedsFileSize(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{
			ContentLength: aws.Int64(100),
			ContentType:   aws.String("video/mp4"),
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	req.Header.Set("Range", "bytes=500-999") // start (500) >= totalSize (100)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, rr.Code)
	assert.Equal(t, "bytes */100", rr.Header().Get("Content-Range"))
	mockS3.AssertNotCalled(t, "GetObject")
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn404_WhenFileNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.MatchedBy(func(in *s3.HeadObjectInput) bool {
		return aws.ToString(in.Key) == "ghost"
	}), mock.Anything).Return(nil, &types.NotFound{})

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/ghost/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "ghost")

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertNotCalled(t, "GetObject")
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn500_WhenHeadObjectFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3: connection refused"))

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "failed to stat file")
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn500_WhenGetObjectFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{
			ContentLength: aws.Int64(1000),
			ContentType:   aws.String("video/mp4"),
		}, nil)

	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3: service unavailable"))

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "failed to retrieve file")
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldReturn404_WhenGetObjectNotFound(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{
			ContentLength: aws.Int64(1000),
			ContentType:   aws.String("video/mp4"),
		}, nil)

	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchKey{})

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "file not found")
	mockS3.AssertExpectations(t)
}

func TestStreamFileHandler_ShouldUseOctetStream_WhenContentTypeNotSetInS3(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	mockS3.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadObjectOutput{
			ContentLength: aws.Int64(10),
			ContentType:   nil, // no content-type stored
		}, nil)

	mockS3.On("GetObject", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.GetObjectOutput{
			Body: io.NopCloser(strings.NewReader("1234567890")),
		}, nil)

	req, _ := http.NewRequest(http.MethodGet, "/files/my-videos/abc123/stream", nil)
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "my-videos", "abc123")

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/octet-stream", rr.Header().Get("Content-Type"))
	mockS3.AssertExpectations(t)
}

// ── parseByteRange ────────────────────────────────────────────────────────────

func TestParseByteRange_ShouldReturnCorrectRange_WhenRangeIsValid(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		totalSize int64
		wantStart int64
		wantEnd   int64
	}{
		{
			name:      "specific range within bounds",
			header:    "bytes=0-499",
			totalSize: 1000,
			wantStart: 0,
			wantEnd:   499,
		},
		{
			name:      "mid-file range",
			header:    "bytes=500-999",
			totalSize: 1000,
			wantStart: 500,
			wantEnd:   999,
		},
		{
			name:      "open-ended range from start",
			header:    "bytes=0-",
			totalSize: 1000,
			wantStart: 0,
			wantEnd:   999,
		},
		{
			name:      "open-ended range from mid-file",
			header:    "bytes=750-",
			totalSize: 1000,
			wantStart: 750,
			wantEnd:   999,
		},
		{
			name:      "suffix range last 100 bytes",
			header:    "bytes=-100",
			totalSize: 1000,
			wantStart: 900,
			wantEnd:   999,
		},
		{
			name:      "suffix range larger than file clamps to zero",
			header:    "bytes=-9999",
			totalSize: 500,
			wantStart: 0,
			wantEnd:   499,
		},
		{
			name:      "range end exceeds size is clamped to last byte",
			header:    "bytes=0-99999",
			totalSize: 1000,
			wantStart: 0,
			wantEnd:   999,
		},
		{
			name:      "single byte at start",
			header:    "bytes=0-0",
			totalSize: 1000,
			wantStart: 0,
			wantEnd:   0,
		},
		{
			name:      "single byte at end",
			header:    "bytes=999-999",
			totalSize: 1000,
			wantStart: 999,
			wantEnd:   999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange + Act
			start, end, err := parseByteRange(tt.header, tt.totalSize)

			// Assert
			assert.NoError(t, err)
			assert.Equal(t, tt.wantStart, start)
			assert.Equal(t, tt.wantEnd, end)
		})
	}
}

func TestParseByteRange_ShouldReturnError_WhenRangeIsInvalid(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		totalSize int64
	}{
		{
			name:      "unsupported range unit",
			header:    "tokens=0-100",
			totalSize: 1000,
		},
		{
			name:      "start byte equals total size",
			header:    "bytes=1000-1999",
			totalSize: 1000,
		},
		{
			name:      "start byte exceeds total size",
			header:    "bytes=5000-9999",
			totalSize: 1000,
		},
		{
			name:      "end is less than start",
			header:    "bytes=500-100",
			totalSize: 1000,
		},
		{
			name:      "non-numeric start",
			header:    "bytes=abc-499",
			totalSize: 1000,
		},
		{
			name:      "non-numeric end",
			header:    "bytes=0-xyz",
			totalSize: 1000,
		},
		{
			name:      "multi-range not supported",
			header:    "bytes=0-100,200-300",
			totalSize: 1000,
		},
		{
			name:      "both start and end empty",
			header:    "bytes=-",
			totalSize: 1000,
		},
		{
			name:      "suffix range of zero bytes",
			header:    "bytes=-0",
			totalSize: 1000,
		},
		{
			name:      "negative start",
			header:    "bytes=-1-499",
			totalSize: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange + Act
			_, _, err := parseByteRange(tt.header, tt.totalSize)

			// Assert
			assert.Error(t, err, "expected parseByteRange to fail for header %q", tt.header)
		})
	}
}

// ── Access control ────────────────────────────────────────────────────────────

func TestStreamFileHandler_ShouldReturn403_WhenBucketNotAllowed(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := &App{S3Client: mockS3, BucketName: "root-bucket", Audit: &NoopAuditProducer{}, Content: &NoopContentProducer{}}

	req, _ := http.NewRequest(http.MethodGet, "/files/forbidden-bucket/key/stream", nil)
	// User is only allowed "my-bucket", not "forbidden-bucket"
	claims := &Claims{AllowedBuckets: []string{"my-bucket"}, Role: "user"}
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, claims))
	rr := httptest.NewRecorder()

	// Act
	app.StreamFileHandler(rr, req, "forbidden-bucket", "key")

	// Assert
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "access denied")
	mockS3.AssertNotCalled(t, "HeadObject")
}
