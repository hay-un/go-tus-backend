package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is the type for values stored in request context.
type contextKey string

const claimsKey contextKey = "claims"

// AllowedClaims holds the JWT claims we care about from the Spring Boot id_token.
type AllowedClaims struct {
	Subject        string   `json:"sub"`
	Email          string   `json:"email"`
	AllowedBuckets []string `json:"allowedBuckets"`
}

// BucketAllowed reports whether the given bucket name is in the allowed list.
// "*" in allowedBuckets grants access to all buckets.
func (c *AllowedClaims) BucketAllowed(bucket string) bool {
	for _, b := range c.AllowedBuckets {
		if b == "*" || b == bucket {
			return true
		}
	}
	return false
}

// ClaimsFromContext retrieves AllowedClaims injected by JWTMiddleware.
// Returns nil if auth is disabled (no SPRING_AUTH_ISSUER set).
func ClaimsFromContext(ctx context.Context) *AllowedClaims {
	v, _ := ctx.Value(claimsKey).(*AllowedClaims)
	return v
}

// ─── JWKS cache ──────────────────────────────────────────────────────────────

// jwksCache fetches and caches the RS256 public key from Spring Boot's JWKS endpoint.
type jwksCache struct {
	mu          sync.RWMutex
	keys        map[string][]byte // kid → DER-encoded public key (unused; we use jwt.ParseRSAPublicKeyFromPEM)
	rsaKeys     map[string]interface{}
	lastFetched time.Time
	ttl         time.Duration
	jwksURL     string
}

func newJWKSCache(issuer string) *jwksCache {
	return &jwksCache{
		rsaKeys: make(map[string]interface{}),
		ttl:     5 * time.Minute,
		jwksURL: strings.TrimSuffix(issuer, "/") + "/.well-known/jwks.json",
	}
}

// getKey returns the public key for the given kid, fetching from JWKS if needed.
func (c *jwksCache) getKey(kid string) (interface{}, error) {
	c.mu.RLock()
	key, ok := c.rsaKeys[kid]
	fresh := time.Since(c.lastFetched) < c.ttl
	c.mu.RUnlock()

	if ok && fresh {
		return key, nil
	}

	// Fetch (and refresh) from Spring Boot
	if err := c.refresh(); err != nil {
		// If refresh fails but we have a cached key, use it
		if ok {
			return key, nil
		}
		return nil, fmt.Errorf("jwks refresh failed: %w", err)
	}

	c.mu.RLock()
	key, ok = c.rsaKeys[kid]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no key found for kid %q", kid)
	}
	return key, nil
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (c *jwksCache) refresh() error {
	resp, err := http.Get(c.jwksURL) //nolint:noctx,gosec — internal service call
	if err != nil {
		return fmt.Errorf("get jwks: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read jwks body: %w", err)
	}

	var jwksResp jwksResponse
	if err := json.Unmarshal(body, &jwksResp); err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}

	newKeys := make(map[string]interface{}, len(jwksResp.Keys))
	for _, k := range jwksResp.Keys {
		if k.Alg != "RS256" || k.Kty != "RSA" {
			continue
		}
		pubKey, err := parseRSAPublicKeyFromJWK(k.N, k.E)
		if err != nil {
			return fmt.Errorf("parse rsa key kid=%s: %w", k.Kid, err)
		}
		newKeys[k.Kid] = pubKey
	}

	c.mu.Lock()
	c.rsaKeys = newKeys
	c.lastFetched = time.Now()
	c.mu.Unlock()

	return nil
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// JWTMiddleware validates the RS256 JWT (issued by Spring Boot) on every request.
// If issuer is empty, the middleware is a no-op (dev mode — auth disabled).
// On success, AllowedClaims are injected into the request context.
func JWTMiddleware(issuer string, next http.Handler) http.Handler {
	if issuer == "" {
		// Auth disabled — pass through (dev/test mode)
		return next
	}

	cache := newJWKSCache(issuer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			jsonError(w, "authorization required", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			kid, _ := t.Header["kid"].(string)
			return cache.getKey(kid)
		}, jwt.WithIssuer(issuer), jwt.WithExpirationRequired())

		if err != nil || !token.Valid {
			jsonError(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		mapClaims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			jsonError(w, "malformed token claims", http.StatusUnauthorized)
			return
		}

		claims := &AllowedClaims{
			Subject: safeString(mapClaims["sub"]),
			Email:   safeString(mapClaims["email"]),
		}

		if raw, ok := mapClaims["allowedBuckets"]; ok {
			if buckets, ok := raw.([]interface{}); ok {
				for _, b := range buckets {
					if s, ok := b.(string); ok {
						claims.AllowedBuckets = append(claims.AllowedBuckets, s)
					}
				}
			}
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// safeString converts an interface{} to string; returns "" if not a string.
func safeString(v interface{}) string {
	s, _ := v.(string)
	return s
}
