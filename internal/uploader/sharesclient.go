package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SharesClient calls go-shares to check share-based access to buckets.
// Results are cached per (bucket, shareeID) with a short TTL.
type SharesClient struct {
	baseURL    string
	secret     string
	httpClient *http.Client
	cache      sync.Map // key: "bucket\x00shareeID" → sharesCacheEntry
}

type sharesCacheEntry struct {
	hasAccess  bool
	permission string
	expiresAt  time.Time
}

const sharesCacheTTL = 30 * time.Second

// NewSharesClient creates a SharesClient pointing at the given go-shares base URL.
func NewSharesClient(baseURL, secret string) *SharesClient {
	return &SharesClient{
		baseURL:    baseURL,
		secret:     secret,
		httpClient: &http.Client{Timeout: 3 * time.Second},
	}
}

// CanAccess returns true if shareeID has share-based access to bucket.
// Results are cached for sharesCacheTTL to avoid per-request HTTP calls.
func (s *SharesClient) CanAccess(ctx context.Context, bucket, shareeID string) (bool, error) {
	cacheKey := bucket + "\x00" + shareeID

	if v, ok := s.cache.Load(cacheKey); ok {
		e := v.(sharesCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.hasAccess, nil
		}
	}

	endpoint := fmt.Sprintf("%s/internal/shares/check?bucket=%s&sharee=%s",
		s.baseURL,
		url.QueryEscape(bucket),
		url.QueryEscape(shareeID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	hasAccess := resp.StatusCode == http.StatusOK

	var body struct {
		HasAccess  bool   `json:"hasAccess"`
		Permission string `json:"permission"`
	}
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck — best-effort parse

	s.cache.Store(cacheKey, sharesCacheEntry{
		hasAccess:  hasAccess,
		permission: body.Permission,
		expiresAt:  time.Now().Add(sharesCacheTTL),
	})

	return hasAccess, nil
}

// InvalidateCache removes cached entries for a specific bucket (e.g. after unshare).
func (s *SharesClient) InvalidateCache(bucket string) {
	s.cache.Range(func(k, _ interface{}) bool {
		if key, ok := k.(string); ok {
			if len(key) > len(bucket) && key[:len(bucket)] == bucket {
				s.cache.Delete(k)
			}
		}
		return true
	})
}

// GetSharesForBucket calls go-shares and returns a list of shares for a given bucket.
func (s *SharesClient) GetSharesForBucket(ctx context.Context, bucket string) ([]map[string]interface{}, error) {
	endpoint := fmt.Sprintf("%s/internal/shares?bucket=%s", s.baseURL, url.QueryEscape(bucket))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []map[string]interface{}{}
	}
	return body.Data, nil
}

// CreateShare calls go-shares to create a new share record.
func (s *SharesClient) CreateShare(ctx context.Context, ownerUserID, ownerBucket, shareeUserID, permission string) (map[string]interface{}, error) {
	payload := fmt.Sprintf(
		`{"ownerUserId":%q,"ownerBucket":%q,"shareeUserId":%q,"permission":%q}`,
		ownerUserID, ownerBucket, shareeUserID, permission,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/internal/shares",
		jsonReader(payload),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("share already exists")
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	return result, nil
}

// DeleteShare calls go-shares to remove a share record.
func (s *SharesClient) DeleteShare(ctx context.Context, bucket, shareeUserID string) error {
	endpoint := fmt.Sprintf("%s/internal/shares?bucket=%s&sharee=%s",
		s.baseURL,
		url.QueryEscape(bucket),
		url.QueryEscape(shareeUserID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("share not found")
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	s.InvalidateCache(bucket)
	return nil
}

// GetSharedBuckets returns all buckets shared with the given sharee.
func (s *SharesClient) GetSharedBuckets(ctx context.Context, shareeID string) ([]map[string]interface{}, error) {
	endpoint := fmt.Sprintf("%s/internal/shares?sharee=%s", s.baseURL, url.QueryEscape(shareeID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []map[string]interface{}{}
	}
	return body.Data, nil
}

// DeleteSharesForBucket calls go-shares to cascade-delete all shares for a bucket.
// Called when a bucket is deleted to prevent stale shares from reappearing on recreation.
func (s *SharesClient) DeleteSharesForBucket(ctx context.Context, bucket string) error {
	endpoint := fmt.Sprintf("%s/internal/shares/bucket/%s", s.baseURL, url.PathEscape(bucket))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	s.InvalidateCache(bucket)
	return nil
}

// IsBucketDeleted checks whether a bucket is soft-deleted (in trash).
// Result is cached for sharesCacheTTL to avoid per-request HTTP calls.
func (s *SharesClient) IsBucketDeleted(ctx context.Context, bucket string) (bool, error) {
	cacheKey := bucket + "\x00__deleted__"
	if v, ok := s.cache.Load(cacheKey); ok {
		e := v.(sharesCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.hasAccess, nil
		}
	}

	endpoint := fmt.Sprintf("%s/internal/buckets/%s/deleted", s.baseURL, url.PathEscape(bucket))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var body struct {
		Deleted bool `json:"deleted"`
	}
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck

	s.cache.Store(cacheKey, sharesCacheEntry{
		hasAccess: body.Deleted,
		expiresAt: time.Now().Add(sharesCacheTTL),
	})

	return body.Deleted, nil
}

// TrashBucket soft-deletes a bucket by calling go-shares.
func (s *SharesClient) TrashBucket(ctx context.Context, bucketName, ownerUserID string, retentionDays int) error {
	payload := fmt.Sprintf(`{"bucketName":%q,"ownerUserId":%q,"retentionDays":%d}`,
		bucketName, ownerUserID, retentionDays)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/internal/buckets/trash",
		jsonReader(payload),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("bucket is already in trash")
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	s.InvalidateCache(bucketName)
	return nil
}

// RestoreBucket removes a bucket from trash in go-shares.
func (s *SharesClient) RestoreBucket(ctx context.Context, bucketName string) error {
	endpoint := fmt.Sprintf("%s/internal/buckets/trash/%s", s.baseURL, url.PathEscape(bucketName))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("bucket not found in trash")
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	s.InvalidateCache(bucketName)
	return nil
}

// GetTrashedBuckets returns all trashed buckets for a given owner.
func (s *SharesClient) GetTrashedBuckets(ctx context.Context, ownerUserID string) ([]map[string]interface{}, error) {
	endpoint := fmt.Sprintf("%s/internal/buckets/trash?ownerUserId=%s", s.baseURL, url.QueryEscape(ownerUserID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []map[string]interface{}{}
	}
	return body.Data, nil
}

// PurgeBucketRecord removes the deleted_buckets DB record after a hard delete.
func (s *SharesClient) PurgeBucketRecord(ctx context.Context, bucketName string) error {
	endpoint := fmt.Sprintf("%s/internal/buckets/trash/%s/purge", s.baseURL, url.PathEscape(bucketName))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("bucket record not found")
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	return nil
}

// DeleteUserShares removes all share records involving a user — both shares they own and
// shares they have been given. Requires two calls because owner_user_id stores a Keycloak
// UUID while sharee_user_id stores an email address.
// Best-effort: errors are logged but never returned so account deletion is not blocked.
func (s *SharesClient) DeleteUserShares(ctx context.Context, userID, email string) {
	if err := s.deleteUserByIdentifier(ctx, userID); err != nil {
		log.Printf("shares: delete owner-side shares for %s: %v", userID, err)
	}
	if err := s.deleteUserByIdentifier(ctx, email); err != nil {
		log.Printf("shares: delete sharee-side shares for %s: %v", email, err)
	}
}

func (s *SharesClient) deleteUserByIdentifier(ctx context.Context, identifier string) error {
	endpoint := fmt.Sprintf("%s/internal/shares/user/%s", s.baseURL, url.PathEscape(identifier))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}
	return nil
}

// SharedLinkRecord is a file-link record returned by go-shares.
type SharedLinkRecord struct {
	ID            string    `json:"id"`
	OwnerUserID   string    `json:"ownerUserId"`
	Bucket        string    `json:"bucket"`
	FileKey       string    `json:"fileKey"`
	PasswordHash  string    `json:"passwordHash"`
	DownloadCount int       `json:"downloadCount"`
	ExpiresAt     time.Time `json:"expiresAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

// SharedLinksPage is a paginated response from go-shares.
type SharedLinksPage struct {
	Data  []SharedLinkRecord `json:"data"`
	Total int                `json:"total"`
	Page  int                `json:"page"`
	Limit int                `json:"limit"`
}

// CreateSharedLinkRecord persists a shared-link record in go-shares and returns the created record.
// passwordHash should be a bcrypt hash, or empty string for password-free links.
func (s *SharesClient) CreateSharedLinkRecord(ctx context.Context, ownerUserID, bucket, fileKey, passwordHash string, expiresAt time.Time) (*SharedLinkRecord, error) {
	payload := fmt.Sprintf(
		`{"ownerUserId":%q,"bucket":%q,"fileKey":%q,"expiresAt":%q,"passwordHash":%q}`,
		ownerUserID, bucket, fileKey, expiresAt.UTC().Format(time.RFC3339), passwordHash,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/internal/shared-links",
		jsonReader(payload),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result SharedLinkRecord
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSharedLinkByID fetches a single shared-link record by ID from go-shares.
// Returns (nil, nil) if not found.
func (s *SharesClient) GetSharedLinkByID(ctx context.Context, id string) (*SharedLinkRecord, error) {
	endpoint := fmt.Sprintf("%s/internal/shared-links/%s", s.baseURL, url.PathEscape(id))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result SharedLinkRecord
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListSharedLinks returns paginated shared-link records for an owner from go-shares.
func (s *SharesClient) ListSharedLinks(ctx context.Context, ownerUserID string, page, limit int) (*SharedLinksPage, error) {
	endpoint := fmt.Sprintf("%s/internal/shared-links?ownerUserId=%s&page=%d&limit=%d",
		s.baseURL, url.QueryEscape(ownerUserID), page, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result SharedLinksPage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Data == nil {
		result.Data = []SharedLinkRecord{}
	}
	return &result, nil
}

// DeleteSharedLink removes a shared-link record from go-shares.
func (s *SharesClient) DeleteSharedLink(ctx context.Context, id, ownerUserID string) error {
	endpoint := fmt.Sprintf("%s/internal/shared-links/%s?ownerUserId=%s",
		s.baseURL, url.PathEscape(id), url.QueryEscape(ownerUserID))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("shared link not found")
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}
	return nil
}

// IncrementDownloadCount notifies go-shares to atomically increment the download counter for a share link.
// Called fire-and-forget after a successful download — errors are non-fatal.
func (s *SharesClient) IncrementDownloadCount(ctx context.Context, id string) error {
	endpoint := fmt.Sprintf("%s/internal/shared-links/%s/downloaded", s.baseURL, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}
	return nil
}

// DeleteSharedLinksByFileKey calls go-shares to remove all shared link records for a specific file.
// Called when a file is deleted so stale records don't appear in the history dashboard.
func (s *SharesClient) DeleteSharedLinksByFileKey(ctx context.Context, bucket, fileKey string) error {
	endpoint := fmt.Sprintf("%s/internal/shared-links?bucket=%s&fileKey=%s",
		s.baseURL, url.QueryEscape(bucket), url.QueryEscape(fileKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}
	return nil
}

// jsonReader returns a strings.Reader for an inline JSON string.
func jsonReader(s string) *strings.Reader { return strings.NewReader(s) }

// ── File Versions ─────────────────────────────────────────────────────────────

// FileVersionRecord is a file version record returned by go-shares.
type FileVersionRecord struct {
	ID         string    `json:"id"`
	Bucket     string    `json:"bucket"`
	Filename   string    `json:"filename"`
	S3Key      string    `json:"s3Key"`
	VersionNum int       `json:"versionNum"`
	Size       int64     `json:"size"`
	ArchivedAt time.Time `json:"archivedAt"`
}

// CreateFileVersion persists a file version record in go-shares.
func (s *SharesClient) CreateFileVersion(ctx context.Context, bucket, filename, s3Key string, size int64) (*FileVersionRecord, error) {
	payload := fmt.Sprintf(
		`{"bucket":%q,"filename":%q,"s3Key":%q,"size":%d}`,
		bucket, filename, s3Key, size,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/internal/file-versions",
		jsonReader(payload),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result FileVersionRecord
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListFileVersions returns all version records for a file in go-shares.
func (s *SharesClient) ListFileVersions(ctx context.Context, bucket, filename string) ([]FileVersionRecord, error) {
	endpoint := fmt.Sprintf("%s/internal/file-versions?bucket=%s&filename=%s",
		s.baseURL, url.QueryEscape(bucket), url.QueryEscape(filename))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var body struct {
		Data []FileVersionRecord `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []FileVersionRecord{}
	}
	return body.Data, nil
}

// GetOldestFileVersion returns the oldest version record for a file, or nil if none exists.
func (s *SharesClient) GetOldestFileVersion(ctx context.Context, bucket, filename string) (*FileVersionRecord, error) {
	endpoint := fmt.Sprintf("%s/internal/file-versions/oldest?bucket=%s&filename=%s",
		s.baseURL, url.QueryEscape(bucket), url.QueryEscape(filename))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var result FileVersionRecord
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CountFileVersions returns the number of version records for a file.
func (s *SharesClient) CountFileVersions(ctx context.Context, bucket, filename string) (int, error) {
	endpoint := fmt.Sprintf("%s/internal/file-versions/count?bucket=%s&filename=%s",
		s.baseURL, url.QueryEscape(bucket), url.QueryEscape(filename))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.Count, nil
}

// DeleteFileVersion removes a file version record from go-shares.
func (s *SharesClient) DeleteFileVersion(ctx context.Context, id string) error {
	endpoint := fmt.Sprintf("%s/internal/file-versions/%s", s.baseURL, url.PathEscape(id))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", s.secret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("go-shares returned %d", resp.StatusCode)
	}
	return nil
}
