package uploader

import (
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

// BucketItemHandler dispatches on /buckets/{name} and /buckets/{name}/rename.
func (a *App) BucketItemHandler(w http.ResponseWriter, r *http.Request) {
	// Strip the "/buckets/" prefix
	rest := strings.TrimPrefix(r.URL.Path, "/buckets/")

	if strings.HasSuffix(rest, "/rename") {
		name := strings.TrimSuffix(rest, "/rename")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.renameBucketHandler(w, r, name)
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
func (a *App) ListBucketsHandler(w http.ResponseWriter, r *http.Request) {
	output, err := a.S3Client.ListBuckets(r.Context(), &s3.ListBucketsInput{})
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to list buckets: %v", err), http.StatusInternalServerError)
		return
	}

	type BucketInfo struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}

	buckets := make([]BucketInfo, 0, len(output.Buckets))
	for _, b := range output.Buckets {
		info := BucketInfo{Name: aws.ToString(b.Name)}
		if b.CreationDate != nil {
			info.CreatedAt = *b.CreationDate
		}
		buckets = append(buckets, info)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buckets)
}

// CreateBucketHandler handles POST /buckets.
// Returns 409 if a bucket with the same name already exists.
func (a *App) CreateBucketHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"name": body.Name})
}

// deleteBucketHandler handles DELETE /buckets/{name}.
// It cascade-deletes all objects inside before removing the bucket.
func (a *App) deleteBucketHandler(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" {
		jsonError(w, "bucket name must not be empty", http.StatusBadRequest)
		return
	}

	// Verify bucket exists
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
}

// renameBucketHandler handles POST /buckets/{name}/rename.
// S3 has no native rename, so we: create new → copy all objects → delete old.
// Returns 409 if the new name is already taken.
func (a *App) renameBucketHandler(w http.ResponseWriter, r *http.Request, oldName string) {
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
	json.NewEncoder(w).Encode(map[string]string{"name": body.NewName})
}
