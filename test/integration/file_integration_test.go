//go:build integration
// +build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeBase64 base64-encodes a string for use in TUS Upload-Metadata.
func encodeBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// TestUploadListDownloadDelete_Integration performs a full file lifecycle:
// create bucket → upload via TUS → list files → download → delete → verify gone.
func TestUploadListDownloadDelete_Integration(t *testing.T) {
	bucketName := uniqueName("filelc")
	mc := newMinioClient(t)

	// 1. Create bucket
	resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, bucketName))
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.True(t, bucketExistsInMinio(t, mc, bucketName))
	defer doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, bucketName))

	// 2. Upload a small file via TUS
	content := []byte("integration test file content for lifecycle test")
	fileSize := len(content)
	client := &http.Client{}

	// TUS POST — create upload in the user bucket
	createReq, err := http.NewRequest("POST", backendURL+"/files/", nil)
	require.NoError(t, err)
	createReq.Header.Set("Tus-Resumable", "1.0.0")
	createReq.Header.Set("Upload-Length", fmt.Sprintf("%d", fileSize))
	createReq.Header.Set("Upload-Metadata", fmt.Sprintf(
		"filename %s,bucket %s",
		encodeBase64("test.txt"),
		encodeBase64(bucketName),
	))

	createResp, err := client.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode, "TUS upload creation must return 201")

	uploadURL := createResp.Header.Get("Location")
	require.NotEmpty(t, uploadURL, "Location header must not be empty")
	t.Logf("Upload URL: %s", uploadURL)

	// Make uploadURL absolute if the server returned a relative path
	if strings.HasPrefix(uploadURL, "/") {
		uploadURL = backendURL + uploadURL
	}

	// TUS PATCH — upload all content in one chunk
	patchReq, err := http.NewRequest("PATCH", uploadURL, strings.NewReader(string(content)))
	require.NoError(t, err)
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
	patchReq.Header.Set("Upload-Offset", "0")

	patchResp, err := client.Do(patchReq)
	require.NoError(t, err)
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusNoContent, patchResp.StatusCode, "TUS chunk upload must return 204")

	// 3. List files in the bucket
	listResp, err := http.Get(fmt.Sprintf("%s/files/?bucket=%s", backendURL, bucketName))
	require.NoError(t, err)
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var files []map[string]interface{}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&files))
	require.Len(t, files, 1, "should have exactly 1 file after upload")

	fileKey, ok := files[0]["key"].(string)
	require.True(t, ok)
	assert.Equal(t, "test.txt", files[0]["name"], "original filename should be preserved")
	assert.NotEmpty(t, fileKey)
	t.Logf("File key: %s, name: %s", fileKey, files[0]["name"])

	// 4. Download the file via backend proxy
	downloadURL := fmt.Sprintf("%s/files/%s/%s", backendURL, bucketName, fileKey)
	dlResp, err := http.Get(downloadURL)
	require.NoError(t, err)
	defer dlResp.Body.Close()
	require.Equal(t, http.StatusOK, dlResp.StatusCode, "download must return 200")

	downloaded, err := io.ReadAll(dlResp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, downloaded, "downloaded content must match uploaded content")
	assert.Contains(t, dlResp.Header.Get("Content-Disposition"), "test.txt")
	t.Logf("✔ Download verified: %d bytes", len(downloaded))

	// 5. Delete the file
	delReq, err := http.NewRequest("DELETE", downloadURL, nil)
	require.NoError(t, err)
	delResp, err := client.Do(delReq)
	require.NoError(t, err)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusNoContent, delResp.StatusCode, "delete must return 204")

	// 6. List should now be empty
	listResp2, err := http.Get(fmt.Sprintf("%s/files/?bucket=%s", backendURL, bucketName))
	require.NoError(t, err)
	defer listResp2.Body.Close()
	require.Equal(t, http.StatusOK, listResp2.StatusCode)

	var files2 []map[string]interface{}
	require.NoError(t, json.NewDecoder(listResp2.Body).Decode(&files2))
	assert.Len(t, files2, 0, "file list should be empty after deletion")

	t.Logf("✔ Full lifecycle (upload→list→download→delete) passed for bucket %q", bucketName)
}

// TestListFiles_RequiresBucket_Integration verifies that GET /files/ without ?bucket returns 400.
func TestListFiles_RequiresBucket_Integration(t *testing.T) {
	resp, err := http.Get(backendURL + "/files/")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Contains(t, body["error"], "bucket query parameter is required")
}

// TestDownloadFile_NotFound_Integration verifies that GET /files/<bucket>/<missing-key> returns 404.
func TestDownloadFile_NotFound_Integration(t *testing.T) {
	bucketName := uniqueName("dnf")

	resp := doPost(t, backendURL+"/buckets", fmt.Sprintf(`{"name":%q}`, bucketName))
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	defer doDelete(t, fmt.Sprintf("%s/buckets/%s", backendURL, bucketName))

	dlResp, err := http.Get(fmt.Sprintf("%s/files/%s/nonexistent-key", backendURL, bucketName))
	require.NoError(t, err)
	defer dlResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, dlResp.StatusCode)
}
