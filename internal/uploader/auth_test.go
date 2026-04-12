package uploader_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codirs/backend/internal/uploader"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Helpers ────────────────────────────────────────────────────────────────

func mustGenerateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k
}

// b64url encodes bytes as base64url without padding.
func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// bigIntBytes returns the minimal big-endian byte representation of n.
func bigIntBytes(n *big.Int) []byte {
	return n.Bytes()
}

// expBytes encodes an RSA public exponent as a big-endian byte slice.
func expBytes(e int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(e))
	// strip leading zeros
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

// jwksForKey returns a JWKS JSON document for the given RSA public key.
func jwksForKey(key *rsa.PrivateKey) map[string]interface{} {
	return map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "test-key",
				"n":   b64url(bigIntBytes(key.PublicKey.N)),
				"e":   b64url(expBytes(key.PublicKey.E)),
			},
		},
	}
}

// startIssuerServer returns a test HTTP server that serves both:
//   - JWKS at /protocol/openid-connect/certs
//
// The server URL is the issuer expected by NewJWTMiddleware.
func startIssuerServer(t *testing.T, key *rsa.PrivateKey) (issuerURL string, cleanup func()) {
	t.Helper()
	jwks := jwksForKey(key)
	mux := http.NewServeMux()
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

// makeToken signs a RS256 JWT with the given key and claims.
func makeToken(t *testing.T, key *rsa.PrivateKey, issuer string, exp time.Time, extra map[string]interface{}) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            issuer,
		"sub":            "user-uuid",
		"email":          "user@test.com",
		"allowedBuckets": []string{"*"},
		"role":           "admin",
		"exp":            exp.Unix(),
		"iat":            time.Now().Add(-time.Second).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	require.NoError(t, err)
	return tok
}

// nopHandler is a stub inner handler that marks called=true.
func nopHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestJWTMiddleware_ShouldBypass_WhenIssuerEmpty(t *testing.T) {
	// Arrange
	called := false
	h := uploader.NewJWTMiddleware("", "", nopHandler(&called))
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)

	// Act
	h.ServeHTTP(w, r)

	// Assert — dev-mode: no token required, inner handler must be invoked
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestJWTMiddleware_ShouldReturn401_WhenNoToken(t *testing.T) {
	// Arrange
	called := false
	key := mustGenerateKey(t)
	issuer, cleanup := startIssuerServer(t, key)
	defer cleanup()

	h := uploader.NewJWTMiddleware(issuer, issuer+"/protocol/openid-connect/certs", nopHandler(&called))
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)

	// Act
	h.ServeHTTP(w, r)

	// Assert
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_ShouldReturn401_WhenTokenExpired(t *testing.T) {
	// Arrange
	called := false
	key := mustGenerateKey(t)
	issuer, cleanup := startIssuerServer(t, key)
	defer cleanup()

	expired := makeToken(t, key, issuer, time.Now().Add(-time.Hour), nil)

	h := uploader.NewJWTMiddleware(issuer, issuer+"/protocol/openid-connect/certs", nopHandler(&called))
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+expired)

	// Act
	h.ServeHTTP(w, r)

	// Assert
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_ShouldReturn401_WhenWrongIssuer(t *testing.T) {
	// Arrange
	called := false
	key := mustGenerateKey(t)
	issuer, cleanup := startIssuerServer(t, key)
	defer cleanup()

	wrongIssuer := makeToken(t, key, "http://wrong-issuer", time.Now().Add(time.Hour), nil)

	h := uploader.NewJWTMiddleware(issuer, issuer+"/protocol/openid-connect/certs", nopHandler(&called))
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+wrongIssuer)

	// Act
	h.ServeHTTP(w, r)

	// Assert
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTMiddleware_ShouldPassThrough_WhenValidToken(t *testing.T) {
	// Arrange
	called := false
	key := mustGenerateKey(t)
	issuer, cleanup := startIssuerServer(t, key)
	defer cleanup()

	valid := makeToken(t, key, issuer, time.Now().Add(time.Hour), nil)

	h := uploader.NewJWTMiddleware(issuer, issuer+"/protocol/openid-connect/certs", nopHandler(&called))
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+valid)

	// Act
	h.ServeHTTP(w, r)

	// Assert
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestJWTMiddleware_ShouldAcceptQueryParamToken_ForStreamEndpoints(t *testing.T) {
	// Arrange
	called := false
	key := mustGenerateKey(t)
	issuer, cleanup := startIssuerServer(t, key)
	defer cleanup()

	valid := makeToken(t, key, issuer, time.Now().Add(time.Hour), nil)

	h := uploader.NewJWTMiddleware(issuer, issuer+"/protocol/openid-connect/certs", nopHandler(&called))
	w := httptest.NewRecorder()
	// No Authorization header — token passed as query param (for <video src>)
	r := httptest.NewRequest(http.MethodGet, "/files/bucket/key/stream?token="+valid, nil)

	// Act
	h.ServeHTTP(w, r)

	// Assert
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, w.Code)
}
