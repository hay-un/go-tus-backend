//go:build integration
// +build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORS_PreflightBuckets_Integration(t *testing.T) {
	req, err := http.NewRequest(http.MethodOptions, backendURL+"/buckets", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode, "OPTIONS preflight must return 204, not 405")
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Content-Type")
}

func TestCORS_PreflightFiles_Integration(t *testing.T) {
	req, err := http.NewRequest(http.MethodOptions, backendURL+"/files/", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Tus-Resumable, Upload-Length, Upload-Metadata, Content-Type")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode, "OPTIONS preflight on /files/ must return 204, not 405")
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Tus-Resumable")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Upload-Metadata")
}

func TestCORS_PreflightBucketDelete_Integration(t *testing.T) {
	req, err := http.NewRequest(http.MethodOptions, backendURL+"/buckets/some-bucket", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "DELETE")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode, "OPTIONS preflight on /buckets/<name> must return 204")
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "DELETE")
}
