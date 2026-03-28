package uploader

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const maxPresignExpiry = 7 * 24 * time.Hour

// Presigner generates presigned S3/MinIO GET URLs.
type Presigner interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// ShareFileHandler generates a temporary presigned download URL for a single file.
// The URL is public — anyone with the link can download without logging in.
//
//	GET /files/share?bucket=x&file=y&expiry=24h
//
// expiry: Go duration string (e.g. "1h", "24h", "168h"). Default: 168h (7 days, MinIO max).
// Returns: { "url": "<presigned-url>", "expiresAt": "<RFC3339>" }
func (a *App) ShareFileHandler(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	file := r.URL.Query().Get("file")
	if bucket == "" || file == "" {
		jsonError(w, "bucket and file query parameters are required", http.StatusBadRequest)
		return
	}

	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/share?bucket="+bucket+"&file="+file, http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	expiry := maxPresignExpiry
	if expiryStr := r.URL.Query().Get("expiry"); expiryStr != "" {
		d, err := time.ParseDuration(expiryStr)
		if err != nil || d <= 0 {
			jsonError(w, "invalid expiry: use Go duration syntax e.g. 1h, 24h, 168h", http.StatusBadRequest)
			return
		}
		if d > maxPresignExpiry {
			d = maxPresignExpiry
		}
		expiry = d
	}

	// Verify file exists before generating presigned URL.
	_, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(file),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to check file", http.StatusInternalServerError)
		return
	}

	req, err := a.Presigner.PresignGetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(file),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		jsonError(w, "failed to generate share link", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().UTC().Add(expiry)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"url":       req.URL,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
	emitAudit(a, r, "file.share", "/files/share?bucket="+bucket+"&file="+file, http.StatusOK)
}
