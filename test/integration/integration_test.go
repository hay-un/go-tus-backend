//go:build integration
// +build integration

package integration

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	baseURL = "http://localhost:8080/files/"
)

func TestResumableUpload_E2E(t *testing.T) {
	// 1. Prepare Data
	content := []byte("Hello, this is a test data for resumable upload integration testing.")
	totalSize := len(content)
	chunkSize := totalSize / 2 // Split into two chunks

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 2. Create Upload (POST)
	req, err := http.NewRequest("POST", baseURL, nil)
	require.NoError(t, err)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(totalSize))
	req.Header.Set("Upload-Metadata", "filename dGVzdF9yZXN1bWUudHh0") // filename test_resume.txt

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	uploadURL := resp.Header.Get("Location")
	require.NotEmpty(t, uploadURL, "Location header should not be empty")

	t.Logf("Upload created at: %s", uploadURL)

	// 3. Upload First Chunk (PATCH) - Simulating Interruption after this
	chunk1 := content[:chunkSize]
	req, err = http.NewRequest("PATCH", uploadURL, bytes.NewReader(chunk1))
	require.NoError(t, err)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")

	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	
	newOffsetStr := resp.Header.Get("Upload-Offset")
	newOffset, err := strconv.Atoi(newOffsetStr)
	require.NoError(t, err)
	require.Equal(t, chunkSize, newOffset, "Offset should match chunk size")

	t.Logf("Chunk 1 uploaded. Server offset: %d", newOffset)

	// 4. Verify Offset (HEAD) - Simulating Resume Check
	req, err = http.NewRequest("HEAD", uploadURL, nil)
	require.NoError(t, err)
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	headOffsetStr := resp.Header.Get("Upload-Offset")
	headOffset, err := strconv.Atoi(headOffsetStr)
	require.NoError(t, err)
	require.Equal(t, chunkSize, headOffset, "HEAD - Offset should match previously uploaded size")

	t.Log("HEAD check passed. Resuming upload...")

	// 5. Upload Second Chunk (PATCH) - Resume
	chunk2 := content[chunkSize:]
	req, err = http.NewRequest("PATCH", uploadURL, bytes.NewReader(chunk2))
	require.NoError(t, err)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", strconv.Itoa(headOffset))

	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	finalOffsetStr := resp.Header.Get("Upload-Offset")
	finalOffset, err := strconv.Atoi(finalOffsetStr)
	require.NoError(t, err)
	require.Equal(t, totalSize, finalOffset, "Final offset should match total size")

	t.Log("Upload completed successfully.")
}
