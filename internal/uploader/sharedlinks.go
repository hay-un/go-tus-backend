package uploader

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListSharedLinksHandler returns a paginated list of files the user has shared via link.
// Each record includes a freshly regenerated presigned URL so the caller can copy/use it.
//
// GET /files/shared-links?page=1&limit=20
func (a *App) ListSharedLinksHandler(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if a.Shares == nil {
		jsonError(w, "sharing feature is disabled", http.StatusServiceUnavailable)
		return
	}

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 100 {
		limit = 20
	}

	result, err := a.Shares.ListSharedLinks(r.Context(), claims.Subject, page, limit)
	if err != nil {
		jsonError(w, "failed to list shared links", http.StatusInternalServerError)
		return
	}

	// Enrich each record with a freshly generated presigned URL and an expired flag.
	type SharedLinkResponse struct {
		ID        string    `json:"id"`
		Bucket    string    `json:"bucket"`
		FileKey   string    `json:"fileKey"`
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expiresAt"`
		CreatedAt time.Time `json:"createdAt"`
		Expired   bool      `json:"expired"`
	}

	now := time.Now().UTC()
	items := make([]SharedLinkResponse, 0, len(result.Data))
	for _, rec := range result.Data {
		item := SharedLinkResponse{
			ID:        rec.ID,
			Bucket:    rec.Bucket,
			FileKey:   rec.FileKey,
			ExpiresAt: rec.ExpiresAt,
			CreatedAt: rec.CreatedAt,
			Expired:   now.After(rec.ExpiresAt),
		}
		// Only regenerate URL for non-expired links; presigning an already-expired link
		// would produce a URL that fails immediately.
		if !item.Expired && a.Presigner != nil {
			remaining := rec.ExpiresAt.Sub(now)
			if remaining > maxPresignExpiry {
				remaining = maxPresignExpiry
			}
			presigned, presignErr := a.Presigner.PresignGetObject(r.Context(), &s3.GetObjectInput{
				Bucket: aws.String(rec.Bucket),
				Key:    aws.String(rec.FileKey),
			}, s3.WithPresignExpires(remaining))
			if presignErr == nil {
				item.URL = presigned.URL
			}
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"data":  items,
		"total": result.Total,
		"page":  result.Page,
		"limit": result.Limit,
	})
}

// DeleteSharedLinkHandler removes a shared-link record from the history.
// Note: this does NOT invalidate the presigned URL itself — if not yet expired it remains valid.
//
// DELETE /files/shared-links/{id}
func (a *App) DeleteSharedLinkHandler(w http.ResponseWriter, r *http.Request, id string) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if a.Shares == nil {
		jsonError(w, "sharing feature is disabled", http.StatusServiceUnavailable)
		return
	}

	id = strings.TrimSpace(id)
	if id == "" {
		jsonError(w, "id is required", http.StatusBadRequest)
		return
	}

	if err := a.Shares.DeleteSharedLink(r.Context(), id, claims.Subject); err != nil {
		if strings.Contains(err.Error(), "not found") {
			jsonError(w, "shared link not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to delete shared link", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
