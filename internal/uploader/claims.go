package uploader

import "context"

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
func (c *Claims) CanAccessBucket(bucket string) bool {
	for _, b := range c.AllowedBuckets {
		if b == "*" {
			return true
		}
		if b == bucket {
			return true
		}
	}
	return false
}

// OwnsBucket reports whether this user is the explicit owner of the bucket.
// The bucket must appear in AllowedBuckets without wildcard, or the user must be an admin.
func (c *Claims) OwnsBucket(bucket string) bool {
	if c.Role == "admin" {
		return true
	}
	for _, b := range c.AllowedBuckets {
		if b != "*" && b == bucket {
			return true
		}
	}
	return false
}
