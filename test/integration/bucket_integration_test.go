//go:build integration
// +build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	backendURL  = "http://localhost:8080"
	minioURL    = "http://localhost:9000"
	minioUser   = "minioadmin"
	minioPass   = "minioadmin"
	minioRegion = "us-east-1"
)

// newMinioClient creates a direct MinIO S3 client for verification.
func newMinioClient(t *testing.T) *s3.Client {
	t.Helper()
	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, opts ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL:           minioURL,
			SigningRegion: minioRegion,
		}, nil
	})
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(minioRegion),
		config.WithEndpointResolverWithOptions(resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(minioUser, minioPass, "")),
	)
	require.NoError(t, err)
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
}

// uniqueName generates a test-scoped bucket name to avoid cross-test collisions.
func uniqueName(prefix string) string {
	return fmt.Sprintf("inttest-%s-%d", prefix, time.Now().UnixNano())
}

// bucketExistsInMinio checks whether a bucket exists directly in MinIO.
func bucketExistsInMinio(t *testing.T, client *s3.Client, name string) bool {
	t.Helper()
	_, err := client.HeadBucket(context.Background(), &s3.HeadBucketInput{
		Bucket: aws.String(name),
	})
	return err == nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func doPost(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func doDelete(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestCreateBucket_Integration creates a bucket via the API and verifies
// it physically exists in MinIO using the S3 SDK.
func TestCreateBucket_Integration(t *testing.T) {
	name := uniqueName("create")
	mc := newMinioClient(t)

	resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, name))
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	// Verify in MinIO directly
	assert.True(t, bucketExistsInMinio(t, mc, name), "bucket should exist in MinIO after create")

	t.Logf("✔ Bucket %q created and verified in MinIO", name)

	// Cleanup
	doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, name))
}

// TestCreateBucket_Duplicate_Integration sends two create requests with the
// same name and asserts the second returns 409.
func TestCreateBucket_Duplicate_Integration(t *testing.T) {
	name := uniqueName("dup")
	mc := newMinioClient(t)

	resp1 := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, name))
	defer resp1.Body.Close()
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	require.True(t, bucketExistsInMinio(t, mc, name))

	// Second request with same name → 409
	resp2 := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, name))
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusConflict, resp2.StatusCode)

	var body map[string]string
	json.NewDecoder(resp2.Body).Decode(&body)
	assert.Contains(t, body["error"], "already exists")

	t.Logf("✔ Duplicate bucket %q correctly rejected with 409", name)

	// Cleanup
	doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, name))
}

// TestDeleteBucket_Integration creates a bucket then deletes it and verifies
// it no longer exists in MinIO.
func TestDeleteBucket_Integration(t *testing.T) {
	name := uniqueName("del")
	mc := newMinioClient(t)

	// Create first
	resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, name))
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.True(t, bucketExistsInMinio(t, mc, name))

	// Delete
	delResp := doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, name))
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// Verify gone in MinIO
	assert.False(t, bucketExistsInMinio(t, mc, name), "bucket should NOT exist in MinIO after delete")

	t.Logf("✔ Bucket %q deleted and confirmed absent in MinIO", name)
}

// TestRenameBucket_Integration renames a bucket and verifies the old name is
// gone and the new name exists in MinIO.
func TestRenameBucket_Integration(t *testing.T) {
	oldName := uniqueName("rename-old")
	newName := uniqueName("rename-new")
	mc := newMinioClient(t)

	// Create old bucket
	resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, oldName))
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.True(t, bucketExistsInMinio(t, mc, oldName))

	// Rename
	renameResp := doPost(t,
		fmt.Sprintf("%s/buckets/%s/rename", backendURL, oldName),
		fmt.Sprintf(`{"new_name":%q}`, newName),
	)
	defer renameResp.Body.Close()
	assert.Equal(t, http.StatusOK, renameResp.StatusCode)

	// Old name must be gone
	assert.False(t, bucketExistsInMinio(t, mc, oldName), "old bucket should NOT exist after rename")
	// New name must exist
	assert.True(t, bucketExistsInMinio(t, mc, newName), "new bucket SHOULD exist after rename")

	t.Logf("✔ Bucket renamed %q → %q, verified in MinIO", oldName, newName)

	// Cleanup
	doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, newName))
}

// TestRenameBucket_NewNameConflict_Integration tries to rename to a name
// that is already taken and expects 409.
func TestRenameBucket_NewNameConflict_Integration(t *testing.T) {
	srcName := uniqueName("conflict-src")
	takenName := uniqueName("conflict-taken")
	mc := newMinioClient(t)

	// Create both buckets
	for _, n := range []string{srcName, takenName} {
		resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, n))
		defer resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		require.True(t, bucketExistsInMinio(t, mc, n))
	}

	// Try rename src → taken → 409
	renameResp := doPost(t,
		fmt.Sprintf("%s/buckets/%s/rename", backendURL, srcName),
		fmt.Sprintf(`{"new_name":%q}`, takenName),
	)
	defer renameResp.Body.Close()
	assert.Equal(t, http.StatusConflict, renameResp.StatusCode)

	var body map[string]string
	json.NewDecoder(renameResp.Body).Decode(&body)
	assert.Contains(t, body["error"], "already exists")

	// srcName should still exist (rename was aborted)
	assert.True(t, bucketExistsInMinio(t, mc, srcName), "source bucket should still exist after failed rename")

	t.Logf("✔ Rename to taken name %q correctly rejected with 409", takenName)

	// Cleanup
	for _, n := range []string{srcName, takenName} {
		doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, n))
	}
}
