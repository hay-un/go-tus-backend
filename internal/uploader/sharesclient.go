package uploader

import (
	"context"
	"encoding/json"
	"fmt"
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

// jsonReader returns a strings.Reader for an inline JSON string.
func jsonReader(s string) *strings.Reader { return strings.NewReader(s) }
