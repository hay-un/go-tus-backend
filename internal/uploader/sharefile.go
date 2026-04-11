package uploader

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/crypto/bcrypt"
)

const maxPresignExpiry = 7 * 24 * time.Hour

// Presigner generates presigned S3/MinIO GET URLs.
type Presigner interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// ShareFileHandler generates a share link for a single file.
// The returned URL is a page URL (`/share/{id}`) that gates access via an optional password.
//
//	GET /files/share?bucket=x&file=y&expiry=24h[&password=secret]
//
// expiry: Go duration string (e.g. "1h", "24h", "168h"). Default: 168h (7 days).
// password: optional secret code; if provided it is bcrypt-hashed before storage.
// Returns: { "shareId": "<uuid>", "expiresAt": "<RFC3339>" }
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

	// Hash the optional password before storage.
	var passwordHash string
	if pw := r.URL.Query().Get("password"); pw != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			jsonError(w, "failed to process password", http.StatusInternalServerError)
			return
		}
		passwordHash = string(hash)
	}

	// Verify file exists before creating the share record.
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

	expiresAt := time.Now().UTC().Add(expiry)
	emitAudit(a, r, "file.share", "/files/share?bucket="+bucket+"&file="+file, http.StatusOK)

	if a.Shares == nil {
		jsonError(w, "sharing feature is disabled", http.StatusServiceUnavailable)
		return
	}

	claims, hasClaims := ClaimsFromContext(r.Context())

	// Persist the shared link record synchronously so we can return the shareId.
	var ownerUserID string
	if hasClaims {
		ownerUserID = claims.Subject
	}

	link, err := a.Shares.CreateSharedLinkRecord(r.Context(), ownerUserID, bucket, file, passwordHash, expiresAt)
	if err != nil {
		log.Printf("shares: failed to persist shared link record: %v", err)
		jsonError(w, "failed to create share link", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"shareId":   link.ID,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
}

