package uploader

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// generateTestRSAKey creates a 2048-bit RSA key pair for testing.
func generateTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

// makeTestJWT signs a JWT with the given private key using RS256.
func makeTestJWT(t *testing.T, key *rsa.PrivateKey, issuer string, allowedBuckets []string, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            issuer,
		"sub":            "user-uuid-123",
		"email":          "user@example.com",
		"allowedBuckets": allowedBuckets,
		"iat":            time.Now().Unix(),
		"exp":            exp.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key-id"

	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

// serveJWKS starts an httptest.Server that returns the given public key as a JWKS endpoint.
func serveJWKS(t *testing.T, pubKey *rsa.PublicKey) *httptest.Server {
	t.Helper()

	n := base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes())

	// Encode exponent as big-endian bytes
	e := pubKey.E
	var eBytes []byte
	for e > 0 {
		eBytes = append([]byte{byte(e & 0xff)}, eBytes...)
		e >>= 8
	}
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	jwksBody, _ := json.Marshal(map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": "test-key-id",
				"n":   n,
				"e":   eB64,
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksBody) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ─── AllowedClaims.BucketAllowed ─────────────────────────────────────────────

func TestBucketAllowed_ShouldReturnTrue_WhenBucketIsInList(t *testing.T) {
	// Arrange
	claims := &AllowedClaims{AllowedBuckets: []string{"videos", "photos"}}

	// Act & Assert
	assert.True(t, claims.BucketAllowed("videos"))
	assert.True(t, claims.BucketAllowed("photos"))
}

func TestBucketAllowed_ShouldReturnFalse_WhenBucketIsNotInList(t *testing.T) {
	// Arrange
	claims := &AllowedClaims{AllowedBuckets: []string{"videos"}}

	// Act & Assert
	assert.False(t, claims.BucketAllowed("photos"))
	assert.False(t, claims.BucketAllowed(""))
}

func TestBucketAllowed_ShouldReturnTrue_WhenWildcardPresent(t *testing.T) {
	// Arrange
	claims := &AllowedClaims{AllowedBuckets: []string{"*"}}

	// Act & Assert
	assert.True(t, claims.BucketAllowed("any-bucket"))
	assert.True(t, claims.BucketAllowed("another-bucket"))
}

func TestBucketAllowed_ShouldReturnFalse_WhenAllowedListIsEmpty(t *testing.T) {
	// Arrange
	claims := &AllowedClaims{AllowedBuckets: []string{}}

	// Act & Assert
	assert.False(t, claims.BucketAllowed("any-bucket"))
}

// ─── parseRSAPublicKeyFromJWK ────────────────────────────────────────────────

func TestParseRSAPublicKeyFromJWK_ShouldReturnPublicKey_WhenInputIsValid(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	pubKey := &privKey.PublicKey

	n := base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes())
	e := pubKey.E
	var eBytes []byte
	for e > 0 {
		eBytes = append([]byte{byte(e & 0xff)}, eBytes...)
		e >>= 8
	}
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	// Act
	parsed, err := parseRSAPublicKeyFromJWK(n, eB64)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, pubKey.N, parsed.N)
	assert.Equal(t, pubKey.E, parsed.E)
}

func TestParseRSAPublicKeyFromJWK_ShouldReturnError_WhenModulusIsInvalidBase64(t *testing.T) {
	// Arrange + Act
	_, err := parseRSAPublicKeyFromJWK("!!!invalid!!!", "AQAB")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode modulus")
}

func TestParseRSAPublicKeyFromJWK_ShouldReturnError_WhenExponentIsInvalidBase64(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.PublicKey.N.Bytes())

	// Act
	_, err := parseRSAPublicKeyFromJWK(nB64, "!!!invalid!!!")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode exponent")
}

// ─── JWTMiddleware ────────────────────────────────────────────────────────────

func TestJWTMiddleware_ShouldPassThrough_WhenIssuerIsEmpty(t *testing.T) {
	// Arrange — empty issuer = dev mode, no auth
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := JWTMiddleware("", next)
	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.True(t, called, "next handler should be called in dev mode")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestJWTMiddleware_ShouldReturn401_WhenAuthorizationHeaderIsMissing(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	jwksSrv := serveJWKS(t, &privKey.PublicKey)
	issuer := jwksSrv.URL

	handler := JWTMiddleware(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	// No Authorization header
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "authorization required")
}

func TestJWTMiddleware_ShouldReturn401_WhenTokenIsMalformed(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	jwksSrv := serveJWKS(t, &privKey.PublicKey)
	issuer := jwksSrv.URL

	handler := JWTMiddleware(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid or expired token")
}

func TestJWTMiddleware_ShouldReturn401_WhenTokenIsExpired(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	jwksSrv := serveJWKS(t, &privKey.PublicKey)
	issuer := jwksSrv.URL

	expiredToken := makeTestJWT(t, privKey, issuer, []string{"*"}, time.Now().Add(-1*time.Hour))

	handler := JWTMiddleware(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid or expired token")
}

func TestJWTMiddleware_ShouldPassThroughAndInjectClaims_WhenTokenIsValid(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	jwksSrv := serveJWKS(t, &privKey.PublicKey)
	issuer := jwksSrv.URL

	validToken := makeTestJWT(t, privKey, issuer, []string{"videos", "photos"}, time.Now().Add(1*time.Hour))

	var capturedClaims *AllowedClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedClaims = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := JWTMiddleware(issuer, next)
	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedClaims, "claims must be injected into context")
	assert.Equal(t, "user-uuid-123", capturedClaims.Subject)
	assert.Equal(t, "user@example.com", capturedClaims.Email)
	assert.Equal(t, []string{"videos", "photos"}, capturedClaims.AllowedBuckets)
}

func TestJWTMiddleware_ShouldReturn401_WhenTokenSignedWithWrongKey(t *testing.T) {
	// Arrange
	rightKey := generateTestRSAKey(t)
	wrongKey := generateTestRSAKey(t)

	jwksSrv := serveJWKS(t, &rightKey.PublicKey) // JWKS has right key's public key
	issuer := jwksSrv.URL

	// Token signed with wrong key
	tokenSignedWithWrongKey := makeTestJWT(t, wrongKey, issuer, []string{"*"}, time.Now().Add(1*time.Hour))

	handler := JWTMiddleware(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+tokenSignedWithWrongKey)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestJWTMiddleware_ShouldReturn401_WhenIssuerDoesNotMatch(t *testing.T) {
	// Arrange
	privKey := generateTestRSAKey(t)
	jwksSrv := serveJWKS(t, &privKey.PublicKey)

	// Token claims issuer "wrong-issuer" but middleware expects jwksSrv.URL
	wrongIssuerToken := makeTestJWT(t, privKey, "https://wrong-issuer.example.com", []string{"*"}, time.Now().Add(1*time.Hour))

	handler := JWTMiddleware(jwksSrv.URL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+wrongIssuerToken)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── ClaimsFromContext ────────────────────────────────────────────────────────

func TestClaimsFromContext_ShouldReturnNil_WhenNoClaimsInContext(t *testing.T) {
	// Arrange
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Act
	claims := ClaimsFromContext(req.Context())

	// Assert
	assert.Nil(t, claims)
}

// ─── jwksCache large-N encoding ──────────────────────────────────────────────

func TestParseRSAPublicKeyFromJWK_ShouldReconstructKeyWithCorrectModulus_WhenKeyIsLarge(t *testing.T) {
	// Arrange — generate a proper 2048-bit key and round-trip through JWK encoding
	privKey := generateTestRSAKey(t)
	pubKey := &privKey.PublicKey

	nB64 := base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes())
	e := pubKey.E
	var eBytes []byte
	for e > 0 {
		eBytes = append([]byte{byte(e & 0xff)}, eBytes...)
		e >>= 8
	}
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	// Act
	parsed, err := parseRSAPublicKeyFromJWK(nB64, eB64)

	// Assert
	require.NoError(t, err)
	// Verify that a token signed with the private key can be verified using the parsed public key
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(privKey)
	require.NoError(t, err)

	parsed2Key := parsed
	_, err = jwt.Parse(signed, func(t *jwt.Token) (interface{}, error) {
		return parsed2Key, nil
	})
	assert.NoError(t, err)
}

// ─── encodeBase64 for big.Int exponent comparison ────────────────────────────

func TestBucketAllowed_ShouldTreatWildcardAndExplicitSeparately(t *testing.T) {
	// Arrange
	noWildcard := &AllowedClaims{AllowedBuckets: []string{"videos", "photos"}}
	withWildcard := &AllowedClaims{AllowedBuckets: []string{"videos", "*"}}

	// Act & Assert — without wildcard, unlisted bucket denied
	assert.False(t, noWildcard.BucketAllowed("secret"))
	// with wildcard, any bucket allowed
	assert.True(t, withWildcard.BucketAllowed("secret"))
}

// ─── encode helpers (used by serveJWKS) ─────────────────────────────────────

func bigIntToBase64URL(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}
