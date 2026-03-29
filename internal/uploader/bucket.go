package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// isBucketNotFound checks whether an S3 error indicates a missing bucket.
func isBucketNotFound(err error) bool {
	var nf *types.NoSuchBucket
	var notFound *types.NotFound
	return errors.As(err, &nf) || errors.As(err, &notFound)
}

// bucketExists probes a bucket with HeadBucket.
// Returns (true, nil) if it exists, (false, nil) if not found, (false, err) on other errors.
func (a *App) bucketExists(r *http.Request, name string) (bool, error) {
	_, err := a.S3Client.HeadBucket(r.Context(), &s3.HeadBucketInput{
		Bucket: aws.String(name),
	})
	if err == nil {
		return true, nil
	}
	if isBucketNotFound(err) {
		return false, nil
	}
	return false, err
}

// BucketsHandler dispatches GET and POST on /buckets.
func (a *App) BucketsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.ListBucketsHandler(w, r)
	case http.MethodPost:
		a.CreateBucketHandler(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// BucketItemHandler dispatches on /buckets/{name}, /buckets/{name}/rename,
// /buckets/{name}/restore, /buckets/trash, and /buckets/{name}/shares[/{sharee}].
func (a *App) BucketItemHandler(w http.ResponseWriter, r *http.Request) {
	// Strip the "/buckets/" prefix
	rest := strings.TrimPrefix(r.URL.Path, "/buckets/")

	// GET /buckets/trash → list trashed buckets
	if rest == "trash" || rest == "trash/" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.ListBucketTrashHandler(w, r)
		return
	}

	if strings.HasSuffix(rest, "/rename") {
		name := strings.TrimSuffix(rest, "/rename")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.renameBucketHandler(w, r, name)
		return
	}

	// POST /buckets/{name}/restore
	if strings.HasSuffix(rest, "/restore") {
		name := strings.TrimSuffix(rest, "/restore")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.RestoreBucketHandler(w, r, name)
		return
	}

	// /buckets/{name}/shares[/{sharee}]
	if strings.Contains(rest, "/shares") {
		a.SharesItemHandler(w, r)
		return
	}

	// /buckets/{name}
	name := strings.TrimSuffix(rest, "/")
	switch r.Method {
	case http.MethodDelete:
		a.deleteBucketHandler(w, r, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ListBucketsHandler handles GET /buckets.
// Returns personal buckets (via JWT allowedBuckets) plus buckets shared with the user (via go-shares).
func (a *App) ListBucketsHandler(w http.ResponseWriter, r *http.Request) {
	output, err := a.S3Client.ListBuckets(r.Context(), &s3.ListBucketsInput{})
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to list buckets: %v", err), http.StatusInternalServerError)
		return
	}

	type BucketInfo struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
		Shared    bool      `json:"shared,omitempty"` // true when bucket belongs to another user
	}

	claims, hasClaims := ClaimsFromContext(r.Context())

	// Build MinIO bucket index for fast CreatedAt lookup.
	minioBuckets := make(map[string]time.Time, len(output.Buckets))
	for _, b := range output.Buckets {
		name := aws.ToString(b.Name)
		if b.CreationDate != nil {
			minioBuckets[name] = *b.CreationDate
		}
	}

	// Build set of trashed bucket names to exclude from listing.
	trashedSet := make(map[string]bool)
	if a.Shares != nil && hasClaims && claims.Subject != "" {
		trashed, err := a.Shares.GetTrashedBuckets(r.Context(), claims.Subject)
		if err == nil {
			for _, t := range trashed {
				if bn, ok := t["bucketName"].(string); ok {
					trashedSet[bn] = true
				}
			}
		}
	}

	buckets := make([]BucketInfo, 0)

	// ── Personal buckets (JWT allowedBuckets) ────────────────────────────────
	for _, b := range output.Buckets {
		name := aws.ToString(b.Name)
		if hasClaims && !claims.CanAccessBucket(name) {
			continue
		}
		if trashedSet[name] {
			continue // bucket is in trash — exclude from listing
		}
		info := BucketInfo{Name: name}
		if b.CreationDate != nil {
			info.CreatedAt = *b.CreationDate
		}
		buckets = append(buckets, info)
	}

	// ── Shared buckets (go-shares) ────────────────────────────────────────────
	if a.Shares != nil && hasClaims && claims.Email != "" {
		sharedEntries, err := a.Shares.GetSharedBuckets(r.Context(), claims.Email)
		if err == nil {
			included := make(map[string]bool, len(buckets))
			for _, b := range buckets {
				included[b.Name] = true
			}
			for _, entry := range sharedEntries {
				bucketName, _ := entry["ownerBucket"].(string)
				if bucketName == "" || included[bucketName] {
					continue
				}
				info := BucketInfo{Name: bucketName, Shared: true, CreatedAt: minioBuckets[bucketName]}
				buckets = append(buckets, info)
				included[bucketName] = true
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buckets) //nolint:errcheck
	emitAudit(a, r, "bucket.list", "/buckets", http.StatusOK)
}

// CreateBucketHandler handles POST /buckets.
// Top-level bucket creation requires admin role.
// Sub-bucket creation (parent field provided) requires the caller to own the parent bucket.
// Sub-buckets are stored as MinIO buckets named "{parent}--{name}".
// Returns 409 if a bucket with the same name already exists.
func (a *App) CreateBucketHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Parent string `json:"parent"` // optional; when set, creates a sub-bucket inside parent
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	claims, hasClaims := ClaimsFromContext(r.Context())

	if body.Parent != "" {
		// Sub-bucket: caller must own the parent bucket
		if !hasClaims {
			jsonError(w, "authentication required to create sub-buckets", http.StatusUnauthorized)
			return
		}
		if !claims.OwnsBucket(body.Parent) {
			jsonError(w, "you do not own the parent bucket", http.StatusForbidden)
			return
		}
		// Full MinIO bucket name: "{parent}--{child}"
		body.Name = body.Parent + "--" + body.Name
	} else {
		// Top-level bucket: requires admin role
		if hasClaims && claims.Role != "admin" {
			jsonError(w, "admin role required to create buckets", http.StatusForbidden)
			return
		}
	}

	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "bucket name must not be empty", http.StatusBadRequest)
		return
	}

	// Duplicate check
	exists, err := a.bucketExists(r, body.Name)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to check bucket: %v", err), http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, fmt.Sprintf("bucket %q already exists", body.Name), http.StatusConflict)
		return
	}

	if _, err := a.S3Client.CreateBucket(r.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(body.Name),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to create bucket: %v", err), http.StatusInternalServerError)
		return
	}

	// Set MinIO lifecycle rule for __trash__/ prefix auto-expiry.
	if a.TrashRetentionDays > 0 {
		a.setTrashLifecycleRule(r.Context(), body.Name)
	}

	// Grant the new sub-bucket in Keycloak so it appears in the user's JWT on next token refresh.
	if body.Parent != "" && a.KeycloakGranter != nil && hasClaims && claims.Email != "" {
		_ = a.KeycloakGranter.GrantBucket(r.Context(), claims.Email, body.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}{Name: body.Name, CreatedAt: time.Now().UTC()})
	emitAudit(a, r, "bucket.create", "/buckets/"+body.Name, http.StatusCreated)
}

// deleteBucketHandler handles DELETE /buckets/{name}.
// When go-shares is configured, performs a soft delete (moves bucket to trash).
// Falls back to hard delete (MinIO + shares cascade) when go-shares is not configured.
// Requires the caller to own the bucket (explicit allowedBuckets entry or admin role).
func (a *App) deleteBucketHandler(w http.ResponseWriter, r *http.Request, name string) {
	claims, hasClaims := ClaimsFromContext(r.Context())
	if hasClaims && !claims.OwnsBucket(name) {
		jsonError(w, "you do not own this bucket", http.StatusForbidden)
		return
	}

	if name == "" {
		jsonError(w, "bucket name must not be empty", http.StatusBadRequest)
		return
	}

	// ── Soft delete (go-shares configured) ───────────────────────────────────
	if a.Shares != nil {
		if !hasClaims {
			jsonError(w, "authentication required to delete buckets", http.StatusUnauthorized)
			return
		}

		exists, err := a.bucketExists(r, name)
		if err != nil {
			jsonError(w, fmt.Sprintf("failed to check bucket: %v", err), http.StatusInternalServerError)
			return
		}
		if !exists {
			jsonError(w, fmt.Sprintf("bucket %q not found", name), http.StatusNotFound)
			return
		}

		if err := a.Shares.TrashBucket(r.Context(), name, claims.Subject, a.TrashRetentionDays); err != nil {
			if strings.Contains(err.Error(), "already in trash") {
				jsonError(w, "bucket is already in trash", http.StatusConflict)
				return
			}
			jsonError(w, fmt.Sprintf("failed to trash bucket: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
		emitAudit(a, r, "bucket.trash", "/buckets/"+name, http.StatusNoContent)
		return
	}

	// ── Hard delete fallback (go-shares not configured) ───────────────────────
	exists, err := a.bucketExists(r, name)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to check bucket: %v", err), http.StatusInternalServerError)
		return
	}
	if !exists {
		jsonError(w, fmt.Sprintf("bucket %q not found", name), http.StatusNotFound)
		return
	}

	// List and delete all objects (S3 requires empty bucket before deletion)
	listOut, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(name),
	})
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to list objects: %v", err), http.StatusInternalServerError)
		return
	}

	if len(listOut.Contents) > 0 {
		objectIds := make([]types.ObjectIdentifier, 0, len(listOut.Contents))
		for _, obj := range listOut.Contents {
			objectIds = append(objectIds, types.ObjectIdentifier{Key: obj.Key})
		}
		if _, err := a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{
			Bucket: aws.String(name),
			Delete: &types.Delete{Objects: objectIds},
		}); err != nil {
			jsonError(w, fmt.Sprintf("failed to delete objects: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if _, err := a.S3Client.DeleteBucket(r.Context(), &s3.DeleteBucketInput{
		Bucket: aws.String(name),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to delete bucket: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	emitAudit(a, r, "bucket.delete", "/buckets/"+name, http.StatusNoContent)
}

// renameBucketHandler handles POST /buckets/{name}/rename.
// S3 has no native rename, so we: create new → copy all objects → delete old.
// Returns 409 if the new name is already taken.
// Requires the caller to own the bucket (explicit allowedBuckets entry or admin role).
func (a *App) renameBucketHandler(w http.ResponseWriter, r *http.Request, oldName string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok && !claims.OwnsBucket(oldName) {
		jsonError(w, "you do not own this bucket", http.StatusForbidden)
		return
	}

	if oldName == "" {
		jsonError(w, "bucket name must not be empty", http.StatusBadRequest)
		return
	}

	var body struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.NewName) == "" {
		jsonError(w, "new_name must not be empty", http.StatusBadRequest)
		return
	}
	if oldName == body.NewName {
		jsonError(w, "new_name must differ from current name", http.StatusBadRequest)
		return
	}

	// Check old bucket exists
	oldExists, err := a.bucketExists(r, oldName)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to check source bucket: %v", err), http.StatusInternalServerError)
		return
	}
	if !oldExists {
		jsonError(w, fmt.Sprintf("bucket %q not found", oldName), http.StatusNotFound)
		return
	}

	// Check new name is not already taken (duplicate guard)
	newExists, err := a.bucketExists(r, body.NewName)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to check destination bucket: %v", err), http.StatusInternalServerError)
		return
	}
	if newExists {
		jsonError(w, fmt.Sprintf("bucket %q already exists", body.NewName), http.StatusConflict)
		return
	}

	// Create new bucket
	if _, err := a.S3Client.CreateBucket(r.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(body.NewName),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to create destination bucket: %v", err), http.StatusInternalServerError)
		return
	}

	// Copy all objects from old to new bucket
	listOut, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(oldName),
	})
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to list objects for copy: %v", err), http.StatusInternalServerError)
		return
	}

	for _, obj := range listOut.Contents {
		key := aws.ToString(obj.Key)
		if _, err := a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{
			Bucket:     aws.String(body.NewName),
			CopySource: aws.String(fmt.Sprintf("%s/%s", oldName, key)),
			Key:        aws.String(key),
		}); err != nil {
			jsonError(w, fmt.Sprintf("failed to copy object %q: %v", key, err), http.StatusInternalServerError)
			return
		}
	}

	// Delete objects from old bucket then delete the bucket itself
	if len(listOut.Contents) > 0 {
		objectIds := make([]types.ObjectIdentifier, 0, len(listOut.Contents))
		for _, obj := range listOut.Contents {
			objectIds = append(objectIds, types.ObjectIdentifier{Key: obj.Key})
		}
		if _, err := a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{
			Bucket: aws.String(oldName),
			Delete: &types.Delete{Objects: objectIds},
		}); err != nil {
			jsonError(w, fmt.Sprintf("failed to delete old objects: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if _, err := a.S3Client.DeleteBucket(r.Context(), &s3.DeleteBucketInput{
		Bucket: aws.String(oldName),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to delete old bucket: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct { //nolint:errcheck
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}{Name: body.NewName, CreatedAt: time.Now().UTC()})
	emitAudit(a, r, "bucket.rename", "/buckets/"+oldName+"/rename", http.StatusOK)
}

// ListBucketTrashHandler handles GET /buckets/trash.
// Returns buckets in trash owned by the authenticated user.
func (a *App) ListBucketTrashHandler(w http.ResponseWriter, r *http.Request) {
	if a.Shares == nil {
		jsonError(w, "trash feature not available", http.StatusServiceUnavailable)
		return
	}

	claims, ok := ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	buckets, err := a.Shares.GetTrashedBuckets(r.Context(), claims.Subject)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to list trashed buckets: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"data": buckets}) //nolint:errcheck
	emitAudit(a, r, "bucket.trash.list", "/buckets/trash", http.StatusOK)
}

// RestoreBucketHandler handles POST /buckets/{name}/restore.
// Removes the bucket from trash so it becomes accessible again.
func (a *App) RestoreBucketHandler(w http.ResponseWriter, r *http.Request, name string) {
	if a.Shares == nil {
		jsonError(w, "trash feature not available", http.StatusServiceUnavailable)
		return
	}

	if claims, ok := ClaimsFromContext(r.Context()); ok && !claims.OwnsBucket(name) {
		jsonError(w, "you do not own this bucket", http.StatusForbidden)
		return
	}

	if name == "" {
		jsonError(w, "bucket name must not be empty", http.StatusBadRequest)
		return
	}

	if err := a.Shares.RestoreBucket(r.Context(), name); err != nil {
		if strings.Contains(err.Error(), "not found in trash") {
			jsonError(w, "bucket not found in trash", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to restore bucket: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	emitAudit(a, r, "bucket.restore", "/buckets/"+name+"/restore", http.StatusNoContent)
}

// setTrashLifecycleRule sets a MinIO lifecycle rule on a bucket so that objects
// under the __trash__/ prefix are automatically hard-deleted after TrashRetentionDays.
// Failure is non-fatal — bucket creation is not blocked.
func (a *App) setTrashLifecycleRule(ctx context.Context, bucket string) {
	days := int32(a.TrashRetentionDays)
	_, err := a.S3Client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{
				{
					ID:     aws.String("trash-expiry"),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{
						Prefix: aws.String("__trash__/"),
					},
					Expiration: &types.LifecycleExpiration{
						Days: aws.Int32(days),
					},
				},
			},
		},
	})
	if err != nil {
		// Non-fatal: lifecycle rule failure should not block bucket creation
		_ = err
	}
}
