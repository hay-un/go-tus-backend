package uploader

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const versionsPrefix = "__versions__/"

// resolveFileName reads the TUS .info sidecar for a given fileKey in a bucket
// and returns the original filename. Falls back to fileKey if not found.
func (a *App) resolveFileName(ctx context.Context, bucket, fileKey string) string {
	infoObj, err := a.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(fileKey + ".info"),
	})
	if err != nil {
		return fileKey
	}
	defer infoObj.Body.Close()
	var info struct {
		MetaData map[string]string `json:"MetaData"`
	}
	if jsonErr := json.NewDecoder(infoObj.Body).Decode(&info); jsonErr == nil {
		if filename, ok := info.MetaData["filename"]; ok && filename != "" {
			return filename
		}
	}
	return fileKey
}

// ArchiveFileVersionHandler archives the current active file to __versions__/ and
// records the version in go-shares.
//
// POST /files/{bucket}/{key}/version
func (a *App) ArchiveFileVersionHandler(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/"+key+"/version", http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	if a.Shares == nil {
		jsonError(w, "versioning feature is disabled", http.StatusServiceUnavailable)
		return
	}

	// Get size via HeadObject
	headOut, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to check file", http.StatusInternalServerError)
		return
	}
	size := int64(0)
	if headOut.ContentLength != nil {
		size = *headOut.ContentLength
	}

	// Resolve filename from .info sidecar
	filename := a.resolveFileName(r.Context(), bucket, key)

	// CopyObject: source → __versions__/key
	versionedKey := versionsPrefix + key
	if _, err := a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + key),
		Key:        aws.String(versionedKey),
	}); err != nil {
		jsonError(w, "failed to archive file", http.StatusInternalServerError)
		return
	}

	// Best-effort: copy .info sidecar
	a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + key + ".info"),
		Key:        aws.String(versionedKey + ".info"),
	})

	// Delete originals
	if _, err := a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(key)},
				{Key: aws.String(key + ".info")},
			},
			Quiet: aws.Bool(true),
		},
	}); err != nil {
		jsonError(w, "failed to remove original file", http.StatusInternalServerError)
		return
	}

	// Record version in go-shares
	rec, err := a.Shares.CreateFileVersion(r.Context(), bucket, filename, key, size)
	if err != nil {
		jsonError(w, "failed to record file version", http.StatusInternalServerError)
		return
	}

	// Enforce max versions: if count > max, delete oldest
	if a.MaxFileVersions > 0 {
		count, err := a.Shares.CountFileVersions(r.Context(), bucket, filename)
		if err == nil && count > a.MaxFileVersions {
			oldest, err := a.Shares.GetOldestFileVersion(r.Context(), bucket, filename)
			if err == nil && oldest != nil {
				oldKey := versionsPrefix + oldest.S3Key
				// Delete from MinIO (best-effort)
				a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{ //nolint:errcheck
					Bucket: aws.String(bucket),
					Delete: &types.Delete{
						Objects: []types.ObjectIdentifier{
							{Key: aws.String(oldKey)},
							{Key: aws.String(oldKey + ".info")},
						},
						Quiet: aws.Bool(true),
					},
				})
				// Delete version record
				if err := a.Shares.DeleteFileVersion(r.Context(), oldest.ID); err != nil {
					log.Printf("versions: failed to delete oldest version record %s: %v", oldest.ID, err)
				}
			}
		}
	}

	emitAudit(a, r, "file.version_archive", "/files/"+bucket+"/"+key+"/version", http.StatusCreated)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
		"id":         rec.ID,
		"versionNum": rec.VersionNum,
		"archivedAt": rec.ArchivedAt,
	})
}

// ListFileVersionsHandler returns all versions for a given filename in a bucket.
//
// GET /files/{bucket}/versions?filename=Y
func (a *App) ListFileVersionsHandler(w http.ResponseWriter, r *http.Request, bucket string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/versions", http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	if a.Shares == nil {
		jsonError(w, "versioning feature is disabled", http.StatusServiceUnavailable)
		return
	}

	filename := r.URL.Query().Get("filename")
	if filename == "" {
		jsonError(w, "filename query parameter is required", http.StatusBadRequest)
		return
	}

	versions, err := a.Shares.ListFileVersions(r.Context(), bucket, filename)
	if err != nil {
		jsonError(w, "failed to list file versions", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"data": versions}) //nolint:errcheck
}

// RestoreFileVersionHandler restores a specific archived version back to active.
//
// POST /files/{bucket}/versions/{id}/restore
func (a *App) RestoreFileVersionHandler(w http.ResponseWriter, r *http.Request, bucket, id string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/versions/"+id+"/restore", http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	if a.Shares == nil {
		jsonError(w, "versioning feature is disabled", http.StatusServiceUnavailable)
		return
	}

	// Find the version record by id — list versions and find the matching one
	// We need the bucket and filename; use a bucket-wide listing approach:
	// First, find the version by scanning all files and listing versions for each,
	// but that's expensive. Instead, list objects in __versions__/ to find the s3Key,
	// then call ListFileVersions for that file.
	// Simpler: scan __versions__/ for all .info files to find the matching version id.
	// Use ListFileVersions approach: list __versions__/ objects, read each .info, get filename,
	// then call ListFileVersions(bucket, filename) to find by id.

	// Strategy: list all objects under __versions__/ and read .info sidecars to collect filenames,
	// then for each unique filename call ListFileVersions until we find id.
	var targetVersion *FileVersionRecord
	var oldKey string

	listOut, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(versionsPrefix),
	})
	if err != nil {
		jsonError(w, "failed to list versions", http.StatusInternalServerError)
		return
	}

	// Collect unique filenames from .info sidecars
	seen := map[string]bool{}
	for _, obj := range listOut.Contents {
		k := aws.ToString(obj.Key)
		if !strings.HasSuffix(k, ".info") {
			continue
		}
		// k = __versions__/<uuid>.info → strip prefix and suffix
		vKey := strings.TrimPrefix(strings.TrimSuffix(k, ".info"), versionsPrefix)
		filename := a.resolveFileName(r.Context(), bucket, versionsPrefix+vKey)
		if seen[filename] {
			continue
		}
		seen[filename] = true
		versions, err := a.Shares.ListFileVersions(r.Context(), bucket, filename)
		if err != nil {
			continue
		}
		for i := range versions {
			if versions[i].ID == id {
				targetVersion = &versions[i]
				oldKey = versions[i].S3Key
				break
			}
		}
		if targetVersion != nil {
			break
		}
	}

	if targetVersion == nil {
		jsonError(w, "file version not found", http.StatusNotFound)
		return
	}

	// Find the current active file with the same filename (if any)
	activeListOut, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	if err == nil {
		for _, obj := range activeListOut.Contents {
			k := aws.ToString(obj.Key)
			if k == "" || strings.HasSuffix(k, ".info") ||
				strings.HasPrefix(k, "__trash__/") ||
				strings.HasPrefix(k, versionsPrefix) {
				continue
			}
			currentFilename := a.resolveFileName(r.Context(), bucket, k)
			if currentFilename == targetVersion.Filename {
				// Archive this current active file first
				headOut, headErr := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
					Bucket: aws.String(bucket),
					Key:    aws.String(k),
				})
				activeSize := int64(0)
				if headErr == nil && headOut.ContentLength != nil {
					activeSize = *headOut.ContentLength
				}
				currentVersionedKey := versionsPrefix + k
				a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
					Bucket:     aws.String(bucket),
					CopySource: aws.String(bucket + "/" + k),
					Key:        aws.String(currentVersionedKey),
				})
				a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
					Bucket:     aws.String(bucket),
					CopySource: aws.String(bucket + "/" + k + ".info"),
					Key:        aws.String(currentVersionedKey + ".info"),
				})
				a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{ //nolint:errcheck
					Bucket: aws.String(bucket),
					Delete: &types.Delete{
						Objects: []types.ObjectIdentifier{
							{Key: aws.String(k)},
							{Key: aws.String(k + ".info")},
						},
						Quiet: aws.Bool(true),
					},
				})
				if _, createErr := a.Shares.CreateFileVersion(r.Context(), bucket, currentFilename, k, activeSize); createErr != nil {
					log.Printf("versions: failed to record active file as version during restore: %v", createErr)
				}
				break
			}
		}
	}

	// Restore the versioned file back to its original key
	versionedKey := versionsPrefix + oldKey
	if _, err := a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + versionedKey),
		Key:        aws.String(oldKey),
	}); err != nil {
		jsonError(w, "failed to restore file", http.StatusInternalServerError)
		return
	}

	// Best-effort: restore .info sidecar
	a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + versionedKey + ".info"),
		Key:        aws.String(oldKey + ".info"),
	})

	// Delete the archived copy
	a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{ //nolint:errcheck
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(versionedKey)},
				{Key: aws.String(versionedKey + ".info")},
			},
			Quiet: aws.Bool(true),
		},
	})

	// Delete the version record
	if err := a.Shares.DeleteFileVersion(r.Context(), id); err != nil {
		log.Printf("versions: failed to delete restored version record %s: %v", id, err)
	}

	emitAudit(a, r, "file.version_restore", "/files/"+bucket+"/versions/"+id+"/restore", http.StatusOK)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"restoredKey": oldKey}) //nolint:errcheck
}

// DeleteFileVersionHandler permanently deletes a specific file version.
//
// DELETE /files/{bucket}/versions/{id}
func (a *App) DeleteFileVersionHandler(w http.ResponseWriter, r *http.Request, bucket, id string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/versions/"+id, http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	if a.Shares == nil {
		jsonError(w, "versioning feature is disabled", http.StatusServiceUnavailable)
		return
	}

	// Find version record by scanning versions in bucket
	var targetVersion *FileVersionRecord

	listOut, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(versionsPrefix),
	})
	if err != nil {
		jsonError(w, "failed to list versions", http.StatusInternalServerError)
		return
	}

	seen := map[string]bool{}
	for _, obj := range listOut.Contents {
		k := aws.ToString(obj.Key)
		if !strings.HasSuffix(k, ".info") {
			continue
		}
		vKey := strings.TrimPrefix(strings.TrimSuffix(k, ".info"), versionsPrefix)
		filename := a.resolveFileName(r.Context(), bucket, versionsPrefix+vKey)
		if seen[filename] {
			continue
		}
		seen[filename] = true
		versions, err := a.Shares.ListFileVersions(r.Context(), bucket, filename)
		if err != nil {
			continue
		}
		for i := range versions {
			if versions[i].ID == id {
				targetVersion = &versions[i]
				break
			}
		}
		if targetVersion != nil {
			break
		}
	}

	if targetVersion == nil {
		jsonError(w, "file version not found", http.StatusNotFound)
		return
	}

	// Delete from MinIO
	versionedKey := versionsPrefix + targetVersion.S3Key
	a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{ //nolint:errcheck
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(versionedKey)},
				{Key: aws.String(versionedKey + ".info")},
			},
			Quiet: aws.Bool(true),
		},
	})

	// Delete version record from go-shares
	if err := a.Shares.DeleteFileVersion(r.Context(), id); err != nil {
		jsonError(w, "failed to delete version record", http.StatusInternalServerError)
		return
	}

	emitAudit(a, r, "file.version_delete", "/files/"+bucket+"/versions/"+id, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}
