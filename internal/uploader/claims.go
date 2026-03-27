package uploader

import (
	"context"
	"strings"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const claimsKey contextKey = "claims"

// Claims holds the parsed Keycloak JWT claims relevant to authorization.
type Claims struct {
	Subject        string   // "sub" — Keycloak user UUID
	Email          string   // "email"
	AllowedBuckets []string // "allowedBuckets" — ["*"] means admin (all buckets)
	Role           string   // "role"
}

// ClaimsFromContext retrieves Claims injected by the JWT middleware.
// Returns (nil, false) when no claims are present (e.g. dev mode bypass).
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)
	return c, ok
}

// CanAccessBucket reports whether the claims grant access to the named bucket.
// Admin claims (AllowedBuckets == ["*"]) grant access to every bucket.
// Owning a parent bucket (e.g. "foo") also grants access to its sub-buckets ("foo--bar").
func (c *Claims) CanAccessBucket(bucket string) bool {
	for _, b := range c.AllowedBuckets {
		if b == "*" {
			return true
		}
		if b == bucket {
			return true
		}
		if strings.HasPrefix(bucket, b+"--") {
			return true
		}
	}
	return false
}

// OwnsBucket reports whether this user is the explicit owner of the bucket.
// The bucket must appear in AllowedBuckets without wildcard, or the user must be an admin.
// Owning a parent bucket also implies ownership of its sub-buckets.
func (c *Claims) OwnsBucket(bucket string) bool {
	if c.Role == "admin" {
		return true
	}
	for _, b := range c.AllowedBuckets {
		if b == "*" {
			continue
		}
		if b == bucket {
			return true
		}
		if strings.HasPrefix(bucket, b+"--") {
			return true
		}
	}
	return false
}
