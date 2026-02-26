package uploader

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// keycloakClaims maps the relevant fields from a Keycloak-issued JWT.
type keycloakClaims struct {
	jwt.RegisteredClaims
	Email          string   `json:"email"`
	AllowedBuckets []string `json:"allowedBuckets"`
	Role           string   `json:"role"`
}

// NewJWTMiddleware returns an HTTP middleware that validates Keycloak RS256 JWTs.
//
// Token resolution order:
//  1. Authorization: Bearer <token> header
//  2. ?token= query parameter (used by <video src> stream URLs)
//
// On success the parsed *Claims are stored in the request context.
// On failure a 401 JSON response is written.
//
// Dev-mode bypass: if issuer is empty the middleware is a no-op and injects
// admin Claims so all handlers continue to work without Keycloak running.
func NewJWTMiddleware(issuer string, next http.Handler) http.Handler {
	if issuer == "" {
		// Dev-mode: bypass auth, inject wildcard claims so handlers work.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dev := &Claims{
				Subject:        "dev-user",
				Email:          "dev@local",
				AllowedBuckets: []string{"*"},
				Role:           "admin",
			}
			ctx := context.WithValue(r.Context(), claimsKey, dev)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	jwksURL := issuer + "/protocol/openid-connect/certs"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := extractToken(r)
		if raw == "" {
			jsonError(w, "missing or malformed authorization token", http.StatusUnauthorized)
			return
		}

		claims, err := validateToken(r.Context(), raw, jwksURL, issuer)
		if err != nil {
			jsonError(w, fmt.Sprintf("invalid token: %v", err), http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractToken reads the Bearer token from the Authorization header.
// Falls back to ?token= query parameter for stream endpoints (<video src>).
func extractToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return r.URL.Query().Get("token")
}

// validateToken fetches the JWKS from Keycloak (cached), parses the JWT and
// returns the extracted Claims on success.
func validateToken(ctx context.Context, rawToken, jwksURL, issuer string) (*Claims, error) {
	// keyfunc.NewDefault fetches and caches the JWKS, auto-refreshing on kid mismatch.
	jwks, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}

	var kc keycloakClaims
	token, err := jwt.ParseWithClaims(rawToken, &kc, jwks.Keyfunc, jwt.WithIssuer(issuer))
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("token is not valid")
	}
	if token.Method.Alg() != "RS256" {
		return nil, fmt.Errorf("unexpected signing algorithm: %s", token.Method.Alg())
	}

	return &Claims{
		Subject:        kc.Subject,
		Email:          kc.Email,
		AllowedBuckets: kc.AllowedBuckets,
		Role:           kc.Role,
	}, nil
}
