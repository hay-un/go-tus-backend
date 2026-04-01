package uploader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockKeycloakServer creates a test Keycloak admin API server.
// tokenResp: JSON string for the token endpoint (or "" to return 500)
// usersResp: JSON string for the users search endpoint
// profileAttrs: list of attribute names already in the user profile
func newMockKeycloakServer(t *testing.T, tokenResp string, usersResp string, profileAttrs []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /realms/master/protocol/openid-connect/token
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenResp == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(tokenResp)) //nolint:errcheck
	})

	// GET /admin/realms/testrealm/users
	mux.HandleFunc("/admin/realms/testrealm/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(usersResp)) //nolint:errcheck
	})

	// PUT /admin/realms/testrealm/users/{id}
	mux.HandleFunc("/admin/realms/testrealm/users/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
		} else if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET/PUT /admin/realms/testrealm/users/profile
	mux.HandleFunc("/admin/realms/testrealm/users/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			attrs := make([]map[string]interface{}, 0, len(profileAttrs))
			for _, name := range profileAttrs {
				attrs = append(attrs, map[string]interface{}{"name": name})
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"attributes": attrs}) //nolint:errcheck
		} else if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newGranter constructs an HTTPKeycloakGranter pointing at srv.URL.
func newGranter(srvURL string) *HTTPKeycloakGranter {
	return &HTTPKeycloakGranter{
		adminBaseURL: srvURL,
		realm:        "testrealm",
		username:     "admin",
		password:     "admin",
	}
}

// ── NewHTTPKeycloakGranter ────────────────────────────────────────────────────

func TestNewHTTPKeycloakGranter_ShouldReturnNil_WhenEmptyArgs(t *testing.T) {
	assert.Nil(t, NewHTTPKeycloakGranter("", "admin", "pass"))
	assert.Nil(t, NewHTTPKeycloakGranter("http://kc/realms/r", "", "pass"))
	assert.Nil(t, NewHTTPKeycloakGranter("http://kc/realms/r", "admin", ""))
}

func TestNewHTTPKeycloakGranter_ShouldReturnNil_WhenNoRealmSegment(t *testing.T) {
	assert.Nil(t, NewHTTPKeycloakGranter("http://kc/no-realm-segment", "admin", "pass"))
}

func TestNewHTTPKeycloakGranter_ShouldReturnGranter_WhenValidArgs(t *testing.T) {
	g := NewHTTPKeycloakGranter("http://kc/realms/myrealm", "admin", "pass")
	require.NotNil(t, g)
	assert.Equal(t, "http://kc", g.adminBaseURL)
	assert.Equal(t, "myrealm", g.realm)
}

// ── GrantBucket ───────────────────────────────────────────────────────────────

func TestGrantBucket_ShouldAddBucket_WhenUserHasNoBuckets(t *testing.T) {
	// Arrange
	tokenJSON := `{"access_token":"fake-token"}`
	usersJSON := `[{"id":"user-uuid","attributes":{}}]`
	srv := newMockKeycloakServer(t, tokenJSON, usersJSON, nil) // no allowed_buckets attr in profile
	g := newGranter(srv.URL)

	// Act
	err := g.GrantBucket(context.Background(), "rosa@example.com", "rosa-files")

	// Assert
	require.NoError(t, err)
}

func TestGrantBucket_ShouldBeIdempotent_WhenBucketAlreadyGranted(t *testing.T) {
	// Arrange — bucket is already in attributes
	tokenJSON := `{"access_token":"fake-token"}`
	usersJSON := `[{"id":"user-uuid","attributes":{"allowed_buckets":["rosa-files"]}}]`
	srv := newMockKeycloakServer(t, tokenJSON, usersJSON, []string{"allowed_buckets"})
	g := newGranter(srv.URL)

	// Act
	err := g.GrantBucket(context.Background(), "rosa@example.com", "rosa-files")

	// Assert — no error, idempotent
	require.NoError(t, err)
}

func TestGrantBucket_ShouldReturnError_WhenTokenFetchFails(t *testing.T) {
	// Arrange — token endpoint returns 401
	srv := newMockKeycloakServer(t, "", `[]`, nil)
	g := newGranter(srv.URL)

	// Act
	err := g.GrantBucket(context.Background(), "rosa@example.com", "rosa-files")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get admin token")
}

func TestGrantBucket_ShouldReturnError_WhenUserNotFound(t *testing.T) {
	// Arrange — users endpoint returns empty list
	tokenJSON := `{"access_token":"fake-token"}`
	usersJSON := `[]` // not found
	srv := newMockKeycloakServer(t, tokenJSON, usersJSON, nil)
	g := newGranter(srv.URL)

	// Act
	err := g.GrantBucket(context.Background(), "notexist@example.com", "bucket")

	// Assert
	require.Error(t, err)
}

// ── DeleteUser ────────────────────────────────────────────────────────────────

func TestDeleteUser_ShouldSucceed_WhenTokenAndDeleteOK(t *testing.T) {
	// Arrange
	tokenJSON := `{"access_token":"fake-token"}`
	srv := newMockKeycloakServer(t, tokenJSON, `[]`, nil)
	g := newGranter(srv.URL)

	// Act
	err := g.DeleteUser(context.Background(), "user-uuid")

	// Assert
	require.NoError(t, err)
}

func TestDeleteUser_ShouldReturnError_WhenTokenFetchFails(t *testing.T) {
	// Arrange
	srv := newMockKeycloakServer(t, "", `[]`, nil)
	g := newGranter(srv.URL)

	// Act
	err := g.DeleteUser(context.Background(), "user-uuid")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get admin token")
}
