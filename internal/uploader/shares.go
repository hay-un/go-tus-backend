package uploader

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SharesItemHandler dispatches sharing endpoints under /buckets/{name}/shares.
//
// Routes:
//
//	GET    /buckets/{name}/shares           → list who has access to {name}
//	POST   /buckets/{name}/shares           → grant access to a user
//	DELETE /buckets/{name}/shares/{sharee}  → revoke access from a user
func (a *App) SharesItemHandler(w http.ResponseWriter, r *http.Request) {
	// Path: /buckets/{name}/shares or /buckets/{name}/shares/{sharee}
	rest := strings.TrimPrefix(r.URL.Path, "/buckets/")
	parts := strings.SplitN(rest, "/shares", 2)
	if len(parts) < 2 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	bucket := strings.Trim(parts[0], "/")
	shareeSegment := strings.Trim(parts[1], "/")

	if bucket == "" {
		jsonError(w, "bucket name is required", http.StatusBadRequest)
		return
	}

	// Only bucket owners can manage shares.
	claims, hasClaims := ClaimsFromContext(r.Context())
	if hasClaims && !claims.OwnsBucket(bucket) {
		jsonError(w, "only the bucket owner can manage shares", http.StatusForbidden)
		return
	}

	if a.Shares == nil {
		jsonError(w, "sharing feature is not configured (GO_SHARES_URL not set)", http.StatusServiceUnavailable)
		return
	}

	switch {
	case r.Method == http.MethodGet && shareeSegment == "":
		a.handleListShares(w, r, bucket)
	case r.Method == http.MethodPost && shareeSegment == "":
		a.handleCreateShare(w, r, bucket, claims)
	case r.Method == http.MethodDelete && shareeSegment != "":
		a.handleDeleteShare(w, r, bucket, shareeSegment)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleListShares handles GET /buckets/{bucket}/shares.
func (a *App) handleListShares(w http.ResponseWriter, r *http.Request, bucket string) {
	shares, err := a.Shares.GetSharesForBucket(r.Context(), bucket)
	if err != nil {
		jsonError(w, "failed to list shares", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"data": shares}) //nolint:errcheck
}

// handleCreateShare handles POST /buckets/{bucket}/shares.
// Body: { "shareeEmail": "rosa@example.com", "permission": "read" | "write" }
func (a *App) handleCreateShare(w http.ResponseWriter, r *http.Request, bucket string, claims *Claims) {
	var body struct {
		ShareeEmail string `json:"shareeEmail"`
		Permission  string `json:"permission"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.ShareeEmail == "" {
		jsonError(w, "shareeEmail is required", http.StatusBadRequest)
		return
	}
	if body.Permission == "" {
		body.Permission = "read"
	}
	if body.Permission != "read" && body.Permission != "write" {
		jsonError(w, "permission must be 'read' or 'write'", http.StatusBadRequest)
		return
	}

	ownerID := ""
	if claims != nil {
		ownerID = claims.Subject
	}

	result, err := a.Shares.CreateShare(r.Context(), ownerID, bucket, body.ShareeEmail, body.Permission)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			jsonError(w, "share already exists", http.StatusConflict)
			return
		}
		jsonError(w, "failed to create share", http.StatusInternalServerError)
		return
	}

	emitAudit(a, r, "bucket.share.create", "/buckets/"+bucket+"/shares", http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

// handleDeleteShare handles DELETE /buckets/{bucket}/shares/{shareeEmail}.
func (a *App) handleDeleteShare(w http.ResponseWriter, r *http.Request, bucket, shareeEmail string) {
	if err := a.Shares.DeleteShare(r.Context(), bucket, shareeEmail); err != nil {
		if strings.Contains(err.Error(), "not found") {
			jsonError(w, "share not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to delete share", http.StatusInternalServerError)
		return
	}

	emitAudit(a, r, "bucket.share.delete", "/buckets/"+bucket+"/shares/"+shareeEmail, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}
