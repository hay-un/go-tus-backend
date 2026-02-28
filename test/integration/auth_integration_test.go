//go:build integration
// +build integration

package integration

// Auth integration tests validate the JWT middleware behaviour end-to-end.
//
// These tests spin up:
//   - An in-process httptest.Server acting as a JWKS endpoint (mock Spring Boot)
//   - A real Go backend server (net/http) wired with JWTMiddleware pointing at the mock JWKS
//   - A real MinIO (running on localhost:9000)
//
// Run with:
//   cd services/go-tus-backend && make test-integration

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"music-streaming/backend/internal/uploader"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func generateAuthTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func signAuthTestToken(t *testing.T, key *rsa.PrivateKey, issuer string, buckets []string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            issuer,
		"sub":            "test-user-uuid",
		"email":          "test@example.com",
		"allowedBuckets": buckets,
		"iat":            time.Now().Unix(),
		"exp":            exp.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "integration-kid"
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}

func startMockJWKSServer(t *testing.T, pubKey *rsa.PublicKey) *httptest.Server {
	t.Helper()

	n := base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes())
	e := pubKey.E
	var eBytes []byte
	for e > 0 {
		eBytes = append([]byte{byte(e & 0xff)}, eBytes...)
		e >>= 8
	}
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	body, _ := json.Marshal(map[string]interface{}{
		"keys": []map[string]interface{}{
			{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "integration-kid", "n": n, "e": eB64},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startAuthTestServer starts a Go backend HTTP server with JWT middleware enabled,
// listening on a random available port. Returns the base URL.
func startAuthTestServer(t *testing.T, issuer string) (string, func()) {
	t.Helper()

	app, err := uploader.NewAppFromEnv()
	require.NoError(t, err, "NewAppFromEnv requires MinIO running on localhost:9000")

	jwtMW := func(h http.Handler) http.Handler {
		return uploader.JWTMiddleware(issuer, h)
	}

	mux := http.NewServeMux()
	mux.Handle("/files/", uploader.CORS(jwtMW(http.HandlerFunc(app.FilesHandler))))
	mux.Handle("/buckets", uploader.CORS(jwtMW(http.HandlerFunc(app.BucketsHandler))))
	mux.Handle("/buckets/", uploader.CORS(jwtMW(http.HandlerFunc(app.BucketItemHandler))))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Listen on a random port to avoid conflicts with the main backend
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck

	baseURL := fmt.Sprintf("http://%s", ln.Addr().String())
	stop := func() { srv.Close() }
	return baseURL, stop
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestAuthMiddleware_ShouldReturn401_WhenRequestHasNoToken(t *testing.T) {
	// Arrange
	privKey := generateAuthTestKey(t)
	jwksSrv := startMockJWKSServer(t, &privKey.PublicKey)
	srvURL, stop := startAuthTestServer(t, jwksSrv.URL)
	defer stop()

	// Act — request without Authorization header
	resp, err := http.Get(srvURL + "/buckets") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()

	// Assert
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthMiddleware_ShouldReturn401_WhenTokenIsExpired(t *testing.T) {
	// Arrange
	privKey := generateAuthTestKey(t)
	jwksSrv := startMockJWKSServer(t, &privKey.PublicKey)
	srvURL, stop := startAuthTestServer(t, jwksSrv.URL)
	defer stop()

	expiredToken := signAuthTestToken(t, privKey, jwksSrv.URL, []string{"*"}, time.Now().Add(-1*time.Hour))

	req, _ := http.NewRequest(http.MethodGet, srvURL+"/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)

	// Act
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Assert
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthMiddleware_ShouldReturn200_WhenValidTokenPresent(t *testing.T) {
	// Arrange — MinIO must be running
	os.Setenv("AWS_ACCESS_KEY_ID", "minioadmin")
	os.Setenv("AWS_SECRET_ACCESS_KEY", "minioadmin")
	os.Setenv("S3_ENDPOINT", "http://localhost:9000")
	os.Setenv("AWS_REGION", "us-east-1")
	os.Setenv("S3_BUCKET", "music-streaming-bucket")

	privKey := generateAuthTestKey(t)
	jwksSrv := startMockJWKSServer(t, &privKey.PublicKey)
	srvURL, stop := startAuthTestServer(t, jwksSrv.URL)
	defer stop()

	validToken := signAuthTestToken(t, privKey, jwksSrv.URL, []string{"*"}, time.Now().Add(1*time.Hour))

	req, _ := http.NewRequest(http.MethodGet, srvURL+"/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)

	// Act
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Assert — 200 OK (buckets list, even if empty)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuthMiddleware_ShouldReturn401_WhenTokenSignedWithWrongKey(t *testing.T) {
	// Arrange
	rightKey := generateAuthTestKey(t)
	wrongKey := generateAuthTestKey(t)

	jwksSrv := startMockJWKSServer(t, &rightKey.PublicKey) // JWKS has right key
	srvURL, stop := startAuthTestServer(t, jwksSrv.URL)
	defer stop()

	// Token signed with wrong key
	badToken := signAuthTestToken(t, wrongKey, jwksSrv.URL, []string{"*"}, time.Now().Add(1*time.Hour))

	req, _ := http.NewRequest(http.MethodGet, srvURL+"/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+badToken)

	// Act
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Assert
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuthMiddleware_ShouldNotProtectHealthEndpoint(t *testing.T) {
	// Arrange
	privKey := generateAuthTestKey(t)
	jwksSrv := startMockJWKSServer(t, &privKey.PublicKey)
	srvURL, stop := startAuthTestServer(t, jwksSrv.URL)
	defer stop()

	// Act — no auth header on /health
	resp, err := http.Get(srvURL + "/health") //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()

	// Assert
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
