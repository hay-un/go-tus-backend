package uploader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/s3store"
)

// S3API defines the interface we need from the AWS S3 SDK.
type S3API interface {
	s3store.S3API
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	// Bucket management
	ListBuckets(ctx context.Context, params *s3.ListBucketsInput, optFns ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	CreateBucket(ctx context.Context, params *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	DeleteBucket(ctx context.Context, params *s3.DeleteBucketInput, optFns ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, params *s3.PutBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	PutBucketEncryption(ctx context.Context, params *s3.PutBucketEncryptionInput, optFns ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error)
}

// App holds the dependencies for the uploader service.
type App struct {
	TusHandler         http.Handler
	S3Client           S3API
	Presigner          Presigner       // nil when S3 client not yet initialized
	BucketName         string
	S3Endpoint         string
	Audit              AuditProducer   // never nil; use NoopAuditProducer in tests
	Shares             *SharesClient   // nil when GO_SHARES_URL not configured
	KeycloakGranter    KeycloakGranter // nil when admin credentials not configured
	VaultClient        *VaultClient    // nil when VAULT_ADDR not configured (SSE-KMS disabled)
	TrashRetentionDays int             // 0 = no lifecycle rule set on bucket creation
	MaxFileVersions    int             // 0 = unlimited; default 5
	tusHandlers        sync.Map        // map[string]http.Handler — per-user-bucket TUS handlers
}

// canAccessBucket returns true if the given claims grant access to bucket.
// Checks bucket trash status first (deleted bucket = zero access for everyone).
// Then checks JWT allowedBuckets, then the user's home bucket derived from email,
// then falls back to go-shares share access.
func (a *App) canAccessBucket(ctx context.Context, claims *Claims, bucket string) bool {
	// Bucket in trash = zero access for owner and all sharees
	if a.Shares != nil {
		if deleted, err := a.Shares.IsBucketDeleted(ctx, bucket); err == nil && deleted {
			return false
		}
	}
	if claims.CanAccessBucket(bucket) {
		return true
	}
	// Allow access if the bucket is the user's own home bucket, derived from their email.
	// This handles the window between first login and the JWT being refreshed to include
	// allowedBuckets (which Keycloak sets after /internal/provision-user runs).
	if claims.Email != "" && isHomeBucket(claims.Email, bucket) {
		return true
	}
	if a.Shares == nil || claims.Email == "" {
		return false
	}
	ok, err := a.Shares.CanAccess(ctx, bucket, claims.Email)
	if err != nil {
		return false // fail closed
	}
	return ok
}

// isHomeBucket returns true if bucket is the user's provisioned home bucket.
// Home bucket = sanitizeUsername(localpart of email) + "-files".
func isHomeBucket(email, bucket string) bool {
	at := strings.Index(email, "@")
	if at <= 0 {
		return false
	}
	return sanitizeUsername(email[:at])+"-files" == bucket
}

// NewAppFromEnv initializes the App using environment variables.
func NewAppFromEnv() (*App, error) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	s3Endpoint := os.Getenv("S3_ENDPOINT")
	region := os.Getenv("AWS_REGION")
	bucketName := os.Getenv("S3_BUCKET")

	// 1. Configure AWS SDK v2
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if s3Endpoint != "" {
			return aws.Endpoint{
				PartitionID:   "aws",
				URL:           s3Endpoint,
				SigningRegion: region,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolverWithOptions(customResolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %w", err)
	}

	// 2. Create S3 Client (internal — Docker network, used for all API calls)
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	tusHandler, err := NewTusHandler(bucketName, s3Client)
	if err != nil {
		return nil, err
	}

	trashRetentionDays := 30
	if v := os.Getenv("TRASH_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			trashRetentionDays = n
		}
	}

	maxFileVersions := 5
	if v := os.Getenv("MAX_FILE_VERSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			maxFileVersions = n
		}
	}

	// 3. Create Presigner — uses public URL if configured so share links are externally accessible.
	// MINIO_PUBLIC_URL should be set to the URL reachable from outside Docker (e.g. Tailscale host).
	// When empty, falls back to S3_ENDPOINT (internal only — links won't work outside Docker).
	presigner := s3.NewPresignClient(s3Client)
	if publicURL := os.Getenv("MINIO_PUBLIC_URL"); publicURL != "" {
		publicResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{PartitionID: "aws", URL: publicURL, SigningRegion: region}, nil
		})
		publicCfg, err := config.LoadDefaultConfig(context.TODO(),
			config.WithRegion(region),
			config.WithEndpointResolverWithOptions(publicResolver),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		)
		if err == nil {
			publicS3 := s3.NewFromConfig(publicCfg, func(o *s3.Options) { o.UsePathStyle = true })
			presigner = s3.NewPresignClient(publicS3)
		}
	}

	return &App{
		TusHandler:         tusHandler,
		S3Client:           s3Client,
		Presigner:          presigner,
		BucketName:         bucketName,
		S3Endpoint:         s3Endpoint,
		Audit:              &NoopAuditProducer{},
		TrashRetentionDays: trashRetentionDays,
		MaxFileVersions:    maxFileVersions,
	}, nil
}

// NewTusHandler creates a Tus handler pointed at the root bucket (legacy/fallback).
func NewTusHandler(bucketName string, s3Client s3store.S3API) (http.Handler, error) {
	store := s3store.New(bucketName, s3Client)

	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	tusHandler, err := handler.NewHandler(handler.Config{
		BasePath:                "/files/",
		StoreComposer:           composer,
		NotifyCompleteUploads:   false,
		RespectForwardedHeaders: true,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create tus handler: %w", err)
	}

	return http.StripPrefix("/files/", tusHandler), nil
}

// NewTusHandlerForBucket creates a Tus handler with a user-bucket-specific BasePath.
// The returned handler expects paths like /files/<bucket>/<upload-id>.
func NewTusHandlerForBucket(bucketName string, s3Client s3store.S3API) (http.Handler, error) {
	store := s3store.New(bucketName, s3Client)

	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	basePath := "/files/" + bucketName + "/"
	tusHandler, err := handler.NewHandler(handler.Config{
		BasePath:                basePath,
		StoreComposer:           composer,
		NotifyCompleteUploads:   false,
		RespectForwardedHeaders: true,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create tus handler for bucket %q: %w", bucketName, err)
	}

	return http.StripPrefix(basePath, tusHandler), nil
}

// GetTusHandlerForBucket returns a cached per-bucket TUS handler, creating it if needed.
func (a *App) GetTusHandlerForBucket(bucket string) (http.Handler, error) {
	if h, ok := a.tusHandlers.Load(bucket); ok {
		return h.(http.Handler), nil
	}

	h, err := NewTusHandlerForBucket(bucket, a.S3Client)
	if err != nil {
		return nil, err
	}

	actual, _ := a.tusHandlers.LoadOrStore(bucket, h)
	return actual.(http.Handler), nil
}

// ExtractBucketFromTUSMetadata parses the Upload-Metadata header and returns the
// base64-decoded value for the "bucket" key. Returns "" if not found or invalid.
func ExtractBucketFromTUSMetadata(header string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, " ", 2)
		if len(kv) == 2 && kv[0] == "bucket" {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(kv[1]))
			if err == nil {
				return string(decoded)
			}
		}
	}
	return ""
}

// FilesHandler is the main dispatcher for all /files/ routes.
//
//	GET  /files/                    → ListFilesHandler (requires ?bucket=)
//	GET  /files/<bucket>/<key>      → DownloadFileHandler
//	DELETE /files/<bucket>/<key>    → DeleteFileHandler
//	POST /files/ (TUS)              → routes to per-bucket TUS handler
//	PATCH/HEAD /files/<bucket>/<id> → routes to per-bucket TUS handler
func (a *App) FilesHandler(w http.ResponseWriter, r *http.Request) {
	isTUS := r.Header.Get("Tus-Resumable") != ""
	path := r.URL.Path

	// ── Non-TUS: GET /files/ → list files ──────────────────────────────────
	if r.Method == http.MethodGet && path == "/files/" && !isTUS {
		a.ListFilesHandler(w, r)
		return
	}

	// ── Non-TUS: GET /files/share → generate presigned URL ─────────────────
	if r.Method == http.MethodGet && path == "/files/share" && !isTUS {
		a.ShareFileHandler(w, r)
		return
	}

	// ── Non-TUS: GET /files/shared-links → list shared link history ─────────
	if r.Method == http.MethodGet && path == "/files/shared-links" && !isTUS {
		a.ListSharedLinksHandler(w, r)
		return
	}

	// ── Non-TUS: DELETE /files/shared-links/{id} → remove shared link record ─
	if r.Method == http.MethodDelete && strings.HasPrefix(path, "/files/shared-links/") && !isTUS {
		id := strings.TrimPrefix(path, "/files/shared-links/")
		a.DeleteSharedLinkHandler(w, r, id)
		return
	}

	// Parse path segments after /files/
	rest := strings.TrimPrefix(path, "/files/")
	parts := strings.SplitN(rest, "/", 2)
	hasTwoParts := len(parts) == 2 && parts[0] != "" && parts[1] != ""

	// ── Non-TUS: GET /files/{bucket}/versions?filename=Y → list file versions ──
	if r.Method == http.MethodGet && strings.HasSuffix(path, "/versions") && !isTUS {
		bucket := strings.TrimPrefix(strings.TrimSuffix(path, "/versions"), "/files/")
		a.ListFileVersionsHandler(w, r, bucket)
		return
	}

	// ── Non-TUS: POST /files/{bucket}/{key}/version → archive version ──────────
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/version") && !isTUS {
		rest2 := strings.TrimPrefix(strings.TrimSuffix(path, "/version"), "/files/")
		parts2 := strings.SplitN(rest2, "/", 2)
		if len(parts2) == 2 {
			a.ArchiveFileVersionHandler(w, r, parts2[0], parts2[1])
			return
		}
	}

	// ── Non-TUS: POST /files/{bucket}/versions/{id}/restore ─────────────────────
	if r.Method == http.MethodPost && strings.Contains(path, "/versions/") && strings.HasSuffix(path, "/restore") && !isTUS {
		trimmed := strings.TrimPrefix(strings.TrimSuffix(path, "/restore"), "/files/")
		parts2 := strings.SplitN(trimmed, "/versions/", 2)
		if len(parts2) == 2 {
			a.RestoreFileVersionHandler(w, r, parts2[0], parts2[1])
			return
		}
	}

	// ── Non-TUS: DELETE /files/{bucket}/versions/{id} ────────────────────────────
	if r.Method == http.MethodDelete && strings.Contains(path, "/versions/") && !isTUS {
		trimmed := strings.TrimPrefix(path, "/files/")
		parts2 := strings.SplitN(trimmed, "/versions/", 2)
		if len(parts2) == 2 && !strings.Contains(parts2[1], "/") {
			a.DeleteFileVersionHandler(w, r, parts2[0], parts2[1])
			return
		}
	}

	// ── Non-TUS: /files/<bucket>/<key> or /files/<bucket>/<key>/stream ─────
	if !isTUS && hasTwoParts {
		bucket, rawKey := parts[0], parts[1]
		switch r.Method {
		case http.MethodGet:
			if rawKey == "trash" {
				a.ListFileTrashHandler(w, r, bucket)
				return
			}
			if key, isStream := strings.CutSuffix(rawKey, "/stream"); isStream {
				a.StreamFileHandler(w, r, bucket, key)
				return
			}
			a.DownloadFileHandler(w, r, bucket, rawKey)
			return
		case http.MethodPost:
			if after, ok := strings.CutPrefix(rawKey, "trash/"); ok {
				if key, ok := strings.CutSuffix(after, "/restore"); ok {
					a.RestoreFileHandler(w, r, bucket, key)
					return
				}
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			a.DeleteFileHandler(w, r, bucket, rawKey)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	// ── TUS: POST /files/ → create upload in user bucket ───────────────────
	if isTUS && r.Method == http.MethodPost && path == "/files/" {
		bucket := ExtractBucketFromTUSMetadata(r.Header.Get("Upload-Metadata"))
		if bucket == "" {
			jsonError(w, "Upload-Metadata must include bucket field", http.StatusBadRequest)
			return
		}

		if claims, ok := ClaimsFromContext(r.Context()); ok {
			if !a.canAccessBucket(r.Context(), claims, bucket) {
				emitAudit(a, r, "file.access_denied", "/files/"+bucket, http.StatusForbidden)
				jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
				return
			}
		}

		h, err := a.GetTusHandlerForBucket(bucket)
		if err != nil {
			jsonError(w, "failed to initialize upload handler", http.StatusInternalServerError)
			return
		}

		// Rewrite path so the per-bucket handler receives /files/<bucket>/
		r.URL.Path = "/files/" + bucket + "/"
		// Emit audit after handing off (fire-and-forget; TUS upload is initiated)
		emitAudit(a, r, "file.upload", "/files/"+bucket, http.StatusCreated)
		h.ServeHTTP(w, r)
		return
	}

	// ── TUS: PATCH / HEAD / OPTIONS on /files/<bucket>/<upload-id> ─────────
	if isTUS && hasTwoParts {
		bucket := parts[0]
		h, err := a.GetTusHandlerForBucket(bucket)
		if err != nil {
			jsonError(w, "failed to initialize upload handler", http.StatusInternalServerError)
			return
		}
		h.ServeHTTP(w, r)
		return
	}

	// Fallback: pass to root TUS handler (OPTIONS discovery, etc.)
	a.TusHandler.ServeHTTP(w, r)
}

// ListFilesHandler returns a JSON list of files in the specified user bucket.
// Requires ?bucket=<name> query parameter.
func (a *App) ListFilesHandler(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		jsonError(w, "bucket query parameter is required", http.StatusBadRequest)
		return
	}

	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/?bucket="+bucket, http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	output, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		if isBucketNotFound(err) {
			jsonError(w, fmt.Sprintf("bucket %q not found", bucket), http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to list objects: %v", err), http.StatusInternalServerError)
		return
	}

	type FileInfo struct {
		Key          string    `json:"key"`
		Name         string    `json:"name"`
		Size         int64     `json:"size"`
		LastModified time.Time `json:"lastModified"`
		URL          string    `json:"url"`
		StreamURL    string    `json:"streamUrl"`
	}

	files := make([]FileInfo, 0)
	for _, obj := range output.Contents {
		key := aws.ToString(obj.Key)
		// Skip .info sidecar files, empty keys, and files in trash
		if key == "" || strings.HasSuffix(key, ".info") || strings.HasPrefix(key, "__trash__/") {
			continue
		}

		// Try to read original filename from TUS .info sidecar
		name := key
		infoObj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key + ".info"),
		})
		if err == nil {
			var info struct {
				MetaData map[string]string `json:"MetaData"`
			}
			if jsonErr := json.NewDecoder(infoObj.Body).Decode(&info); jsonErr == nil {
				if filename, ok := info.MetaData["filename"]; ok && filename != "" {
					name = filename
				}
			}
			infoObj.Body.Close()
		}

		var lastModified time.Time
		if obj.LastModified != nil {
			lastModified = *obj.LastModified
		}

		// Backend proxy URLs — frontend calls these to download or stream.
		downloadURL := fmt.Sprintf("/files/%s/%s", bucket, key)
		streamURL := fmt.Sprintf("/files/%s/%s/stream", bucket, key)

		files = append(files, FileInfo{
			Key:          key,
			Name:         name,
			Size:         aws.ToInt64(obj.Size),
			LastModified: lastModified,
			URL:          downloadURL,
			StreamURL:    streamURL,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	emitAudit(a, r, "file.list", "/files/?bucket="+bucket, http.StatusOK)
}

// DownloadFileHandler streams a file from the user bucket to the client.
// GET /files/<bucket>/<key>
func (a *App) DownloadFileHandler(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/"+key, http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	// Retrieve original filename from TUS .info sidecar (best-effort)
	filename := key
	infoObj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key + ".info"),
	})
	if err == nil {
		var info struct {
			MetaData map[string]string `json:"MetaData"`
		}
		if jsonErr := json.NewDecoder(infoObj.Body).Decode(&info); jsonErr == nil {
			if fn, ok := info.MetaData["filename"]; ok && fn != "" {
				filename = fn
			}
		}
		infoObj.Body.Close()
	}

	// Get the actual file object
	obj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to retrieve file: %v", err), http.StatusInternalServerError)
		return
	}
	defer obj.Body.Close()

	// Set response headers
	contentType := "application/octet-stream"
	if obj.ContentType != nil && *obj.ContentType != "" {
		contentType = *obj.ContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeFilename(filename)))
	if obj.ContentLength != nil && *obj.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(*obj.ContentLength, 10))
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, obj.Body) //nolint:errcheck — client disconnect is expected
	emitAudit(a, r, "file.download", "/files/"+bucket+"/"+key, http.StatusOK)
}

// DeleteFileHandler deletes a file and its TUS .info sidecar from the user bucket.
// DELETE /files/<bucket>/<key>
func (a *App) DeleteFileHandler(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/"+key, http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	// Verify the file exists before attempting deletion
	_, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to check file: %v", err), http.StatusInternalServerError)
		return
	}

	// Soft delete: move file to __trash__/ prefix
	trashKey := "__trash__/" + key
	if _, err = a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + key),
		Key:        aws.String(trashKey),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to move file to trash: %v", err), http.StatusInternalServerError)
		return
	}
	// Best-effort: copy .info sidecar to trash
	a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + key + ".info"),
		Key:        aws.String(trashKey + ".info"),
	})
	// Delete originals
	if _, err = a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(key)},
				{Key: aws.String(key + ".info")},
			},
		},
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to remove original file: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	emitAudit(a, r, "file.delete", "/files/"+bucket+"/"+key, http.StatusNoContent)

	// Best-effort: remove shared link records so deleted files don't appear in the share history.
	if a.Shares != nil {
		go func() {
			if err := a.Shares.DeleteSharedLinksByFileKey(context.Background(), bucket, key); err != nil {
				log.Printf("shares: failed to cleanup shared links for deleted file %s/%s: %v", bucket, key, err)
			}
		}()
	}
}

// ListFileTrashHandler lists files in the __trash__/ prefix of a user bucket.
// GET /files/<bucket>/trash
func (a *App) ListFileTrashHandler(w http.ResponseWriter, r *http.Request, bucket string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/trash", http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	output, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("__trash__/"),
	})
	if err != nil {
		if isBucketNotFound(err) {
			jsonError(w, fmt.Sprintf("bucket %q not found", bucket), http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to list trash: %v", err), http.StatusInternalServerError)
		return
	}

	type TrashFileInfo struct {
		Key       string    `json:"key"`
		Name      string    `json:"name"`
		Size      int64     `json:"size"`
		DeletedAt time.Time `json:"deletedAt"`
	}

	files := make([]TrashFileInfo, 0)
	for _, obj := range output.Contents {
		trashKey := aws.ToString(obj.Key)
		if trashKey == "" || strings.HasSuffix(trashKey, ".info") {
			continue
		}
		originalKey := strings.TrimPrefix(trashKey, "__trash__/")

		name := originalKey
		infoObj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(trashKey + ".info"),
		})
		if err == nil {
			var info struct {
				MetaData map[string]string `json:"MetaData"`
			}
			if jsonErr := json.NewDecoder(infoObj.Body).Decode(&info); jsonErr == nil {
				if filename, ok := info.MetaData["filename"]; ok && filename != "" {
					name = filename
				}
			}
			infoObj.Body.Close()
		}

		var deletedAt time.Time
		if obj.LastModified != nil {
			deletedAt = *obj.LastModified
		}

		files = append(files, TrashFileInfo{
			Key:       originalKey,
			Name:      name,
			Size:      aws.ToInt64(obj.Size),
			DeletedAt: deletedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"data": files}) //nolint:errcheck
	emitAudit(a, r, "file.trash.list", "/files/"+bucket+"/trash", http.StatusOK)
}

// RestoreFileHandler moves a file from __trash__/ back to its original location.
// POST /files/<bucket>/trash/<key>/restore
func (a *App) RestoreFileHandler(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		if !a.canAccessBucket(r.Context(), claims, bucket) {
			emitAudit(a, r, "file.access_denied", "/files/"+bucket+"/trash/"+key+"/restore", http.StatusForbidden)
			jsonError(w, "access denied to bucket "+bucket, http.StatusForbidden)
			return
		}
	}

	trashKey := "__trash__/" + key

	// Verify file is in trash
	if _, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(trashKey),
	}); err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found in trash", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to check file: %v", err), http.StatusInternalServerError)
		return
	}

	// Check no conflict at original location
	if _, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}); err == nil {
		jsonError(w, "a file with the same name already exists", http.StatusConflict)
		return
	} else if !isObjectNotFound(err) {
		jsonError(w, fmt.Sprintf("failed to check original location: %v", err), http.StatusInternalServerError)
		return
	}

	// Copy back to original location
	if _, err := a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + trashKey),
		Key:        aws.String(key),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to restore file: %v", err), http.StatusInternalServerError)
		return
	}
	// Best-effort: restore .info sidecar
	a.S3Client.CopyObject(r.Context(), &s3.CopyObjectInput{ //nolint:errcheck
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + trashKey + ".info"),
		Key:        aws.String(key + ".info"),
	})
	// Delete trash copies
	a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{ //nolint:errcheck
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(trashKey)},
				{Key: aws.String(trashKey + ".info")},
			},
		},
	})

	w.WriteHeader(http.StatusNoContent)
	emitAudit(a, r, "file.restore", "/files/"+bucket+"/trash/"+key+"/restore", http.StatusNoContent)
}

// isObjectNotFound returns true when an S3 error indicates a missing key.
func isObjectNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var notFound *types.NotFound
	return errors.As(err, &nsk) || errors.As(err, &notFound)
}

// sanitizeFilename removes characters that could break Content-Disposition headers.
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(`"`, `\"`, "\n", "", "\r", "")
	return replacer.Replace(name)
}
