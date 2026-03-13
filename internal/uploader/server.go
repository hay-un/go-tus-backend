package uploader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

// App holds the dependencies for the uploader service.
type App struct {
	TusHandler      http.Handler
	S3Client        S3API
	BucketName      string
	S3Endpoint      string
	Audit           AuditProducer            // never nil; use NoopAuditProducer in tests
	Shares          *SharesClient            // nil when GO_SHARES_URL not configured
	KeycloakGranter KeycloakGranter          // nil when admin credentials not configured
	tusHandlers     sync.Map                 // map[string]http.Handler — per-user-bucket TUS handlers
}

// canAccessBucket returns true if the given claims grant access to bucket.
// It checks JWT allowedBuckets first; falls back to go-shares if configured.
// Sharees are identified by their email address.
func (a *App) canAccessBucket(ctx context.Context, claims *Claims, bucket string) bool {
	if claims.CanAccessBucket(bucket) {
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

	// 2. Create S3 Client
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	tusHandler, err := NewTusHandler(bucketName, s3Client)
	if err != nil {
		return nil, err
	}

	return &App{
		TusHandler: tusHandler,
		S3Client:   s3Client,
		BucketName: bucketName,
		S3Endpoint: s3Endpoint,
		Audit:      &NoopAuditProducer{},
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

	// Parse path segments after /files/
	rest := strings.TrimPrefix(path, "/files/")
	parts := strings.SplitN(rest, "/", 2)
	hasTwoParts := len(parts) == 2 && parts[0] != "" && parts[1] != ""

	// ── Non-TUS: /files/<bucket>/<key> or /files/<bucket>/<key>/stream ─────
	if !isTUS && hasTwoParts {
		bucket, rawKey := parts[0], parts[1]
		switch r.Method {
		case http.MethodGet:
			if key, isStream := strings.CutSuffix(rawKey, "/stream"); isStream {
				a.StreamFileHandler(w, r, bucket, key)
				return
			}
			a.DownloadFileHandler(w, r, bucket, rawKey)
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
		// Skip .info sidecar files and empty keys
		if key == "" || strings.HasSuffix(key, ".info") {
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

	// Batch-delete: file + .info sidecar
	_, err = a.S3Client.DeleteObjects(r.Context(), &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{
				{Key: aws.String(key)},
				{Key: aws.String(key + ".info")},
			},
		},
	})
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to delete file: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	emitAudit(a, r, "file.delete", "/files/"+bucket+"/"+key, http.StatusNoContent)
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
