package uploader

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/crypto/bcrypt"
)

// shareDownloadPresignTTL is the TTL for presigned download URLs generated on password verification.
// Short to prevent forwarding the presigned URL as a bypass.
const shareDownloadPresignTTL = 5 * time.Minute

// ShareDownloadHandler serves public share link endpoints — no JWT required.
//
//	GET  /share/{id} → { requiresPassword, fileName, expiresAt, expired }
//	POST /share/{id} body: { "password": "..." } → { downloadUrl }
func (a *App) ShareDownloadHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/share/")
	id = strings.Trim(id, "/")
	if id == "" {
		jsonError(w, "share id is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.handleShareInfo(w, r, id)
	case http.MethodPost:
		a.handleShareDownload(w, r, id)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleShareInfo returns metadata about a share link without triggering a download.
// GET /share/{id}
func (a *App) handleShareInfo(w http.ResponseWriter, r *http.Request, id string) {
	if a.Shares == nil {
		jsonError(w, "sharing feature is disabled", http.StatusServiceUnavailable)
		return
	}

	link, err := a.Shares.GetSharedLinkByID(r.Context(), id)
	if err != nil {
		jsonError(w, "failed to fetch share link", http.StatusInternalServerError)
		return
	}
	if link == nil {
		jsonError(w, "share link not found", http.StatusNotFound)
		return
	}
	if time.Now().UTC().After(link.ExpiresAt) {
		jsonError(w, "share link has expired", http.StatusGone)
		return
	}

	fileName := a.resolveFileName(r.Context(), link.Bucket, link.FileKey)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"requiresPassword": link.PasswordHash != "",
		"fileName":         fileName,
		"expiresAt":        link.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleShareDownload verifies the password (if required) and returns a short-lived download URL.
// POST /share/{id}
func (a *App) handleShareDownload(w http.ResponseWriter, r *http.Request, id string) {
	if a.Shares == nil {
		jsonError(w, "sharing feature is disabled", http.StatusServiceUnavailable)
		return
	}

	link, err := a.Shares.GetSharedLinkByID(r.Context(), id)
	if err != nil {
		jsonError(w, "failed to fetch share link", http.StatusInternalServerError)
		return
	}
	if link == nil {
		jsonError(w, "share link not found", http.StatusNotFound)
		return
	}
	if time.Now().UTC().After(link.ExpiresAt) {
		jsonError(w, "share link has expired", http.StatusGone)
		return
	}

	// Verify password if one is set.
	if link.PasswordHash != "" {
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
			jsonError(w, "password is required", http.StatusUnauthorized)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(link.PasswordHash), []byte(body.Password)); err != nil {
			jsonError(w, "incorrect password", http.StatusUnauthorized)
			return
		}
	}

	// Generate fresh presigned URL with short TTL.
	if a.Presigner == nil {
		jsonError(w, "presigner not configured", http.StatusInternalServerError)
		return
	}

	presigned, err := a.Presigner.PresignGetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(link.Bucket),
		Key:    aws.String(link.FileKey),
	}, s3.WithPresignExpires(shareDownloadPresignTTL))
	if err != nil {
		jsonError(w, "failed to generate download URL", http.StatusInternalServerError)
		return
	}

	// Fire-and-forget: increment download count in go-shares.
	go func() {
		if err := a.Shares.IncrementDownloadCount(context.Background(), id); err != nil {
			log.Printf("share download: failed to increment count for %s: %v", id, err)
		}
	}()

	emitAudit(a, r, "file.share_download", "/share/"+id, http.StatusOK)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"downloadUrl": presigned.URL,
	})
}
