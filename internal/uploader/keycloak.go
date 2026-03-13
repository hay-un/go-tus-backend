package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// KeycloakGranter sets a user's allowed_buckets attribute in Keycloak after bucket provisioning.
// A nil KeycloakGranter skips this step (dev/test mode or when admin credentials are not configured).
type KeycloakGranter interface {
	GrantBucket(ctx context.Context, email, bucket string) error
}

// HTTPKeycloakGranter calls the Keycloak admin API using admin credentials.
type HTTPKeycloakGranter struct {
	adminBaseURL string // e.g. http://keycloak:8080/auth
	realm        string // e.g. codirs
	username     string
	password     string
}

// NewHTTPKeycloakGranter constructs an HTTPKeycloakGranter from the internal issuer URL
// (e.g. http://keycloak:8080/auth/realms/codirs) and admin credentials.
// Returns nil if any argument is empty — callers treat nil as "granting disabled".
func NewHTTPKeycloakGranter(internalIssuer, username, password string) *HTTPKeycloakGranter {
	if internalIssuer == "" || username == "" || password == "" {
		return nil
	}
	parts := strings.SplitN(internalIssuer, "/realms/", 2)
	if len(parts) != 2 {
		return nil
	}
	return &HTTPKeycloakGranter{
		adminBaseURL: parts[0],
		realm:        parts[1],
		username:     username,
		password:     password,
	}
}

// GrantBucket ensures bucket appears in the user's allowed_buckets Keycloak attribute.
// It is idempotent: if the bucket is already present, it is a no-op.
func (g *HTTPKeycloakGranter) GrantBucket(ctx context.Context, email, bucket string) error {
	token, err := g.getAdminToken(ctx)
	if err != nil {
		return fmt.Errorf("get admin token: %w", err)
	}

	// Ensure allowed_buckets is declared in the user profile schema.
	// Keycloak 24 requires custom attributes to be declared before they can be stored.
	if err := g.ensureUserProfileAttr(ctx, token); err != nil {
		return fmt.Errorf("ensure user profile attr: %w", err)
	}

	userID, attrs, err := g.getUserAttrs(ctx, token, email)
	if err != nil {
		return fmt.Errorf("get user %s: %w", email, err)
	}

	// Idempotent: skip if bucket already granted.
	for _, b := range attrs["allowed_buckets"] {
		if b == bucket {
			return nil
		}
	}
	attrs["allowed_buckets"] = append(attrs["allowed_buckets"], bucket)

	if err := g.updateUserAttrs(ctx, token, userID, attrs); err != nil {
		return fmt.Errorf("update user attrs: %w", err)
	}
	return nil
}

func (g *HTTPKeycloakGranter) getAdminToken(ctx context.Context) (string, error) {
	tokenURL := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", g.adminBaseURL)
	form := url.Values{
		"client_id":  {"admin-cli"},
		"grant_type": {"password"},
		"username":   {g.username},
		"password":   {g.password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func (g *HTTPKeycloakGranter) getUserAttrs(ctx context.Context, token, email string) (string, map[string][]string, error) {
	// Search by email field first; fall back to username (Google IdP may store email as username only).
	for _, param := range []string{"email", "username"} {
		usersURL := fmt.Sprintf("%s/admin/realms/%s/users?%s=%s&exact=true",
			g.adminBaseURL, g.realm, param, url.QueryEscape(email))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, usersURL, nil)
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", nil, err
		}
		var users []struct {
			ID         string              `json:"id"`
			Attributes map[string][]string `json:"attributes"`
		}
		err = json.NewDecoder(resp.Body).Decode(&users)
		resp.Body.Close() //nolint:errcheck
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		if len(users) == 0 {
			continue
		}
		u := users[0]
		if u.Attributes == nil {
			u.Attributes = map[string][]string{}
		}
		return u.ID, u.Attributes, nil
	}
	return "", nil, fmt.Errorf("user not found by email or username: %s", email)
}

func (g *HTTPKeycloakGranter) updateUserAttrs(ctx context.Context, token, userID string, attrs map[string][]string) error {
	userURL := fmt.Sprintf("%s/admin/realms/%s/users/%s", g.adminBaseURL, g.realm, userID)
	data, err := json.Marshal(map[string]interface{}{"attributes": attrs})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, userURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("update user endpoint returned %d", resp.StatusCode)
	}
	return nil
}

// ensureUserProfileAttr ensures the allowed_buckets attribute is declared in the Keycloak
// user profile schema. Keycloak 24+ requires custom attributes to be declared before they
// can be stored on users. This call is idempotent — it skips if already declared.
func (g *HTTPKeycloakGranter) ensureUserProfileAttr(ctx context.Context, token string) error {
	profileURL := fmt.Sprintf("%s/admin/realms/%s/users/profile", g.adminBaseURL, g.realm)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	var profile map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&profile)
	resp.Body.Close() //nolint:errcheck
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get user profile returned %d", resp.StatusCode)
	}

	attrs, _ := profile["attributes"].([]interface{})
	for _, a := range attrs {
		if am, ok := a.(map[string]interface{}); ok {
			if am["name"] == "allowed_buckets" {
				return nil // already declared
			}
		}
	}

	// Not declared yet — add it as admin-only, multivalued.
	newAttr := map[string]interface{}{
		"name":        "allowed_buckets",
		"displayName": "Allowed Buckets",
		"multivalued": true,
		"permissions": map[string]interface{}{
			"view": []string{"admin"},
			"edit": []string{"admin"},
		},
		"annotations": map[string]interface{}{},
	}
	profile["attributes"] = append(attrs, newAttr)

	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, profileURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update user profile returned %d", resp.StatusCode)
	}
	return nil
}
