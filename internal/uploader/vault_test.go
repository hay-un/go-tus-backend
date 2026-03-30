package uploader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeVault creates a fake Vault HTTP server for testing.
// handlers maps path → handler. Unregistered paths return 404.
func fakeVault(handlers map[string]http.HandlerFunc) *httptest.Server {
	mux := http.NewServeMux()
	for path, h := range handlers {
		mux.Handle(path, h)
	}
	return httptest.NewServer(mux)
}

// ── NewVaultClient ────────────────────────────────────────────────────────────

func TestNewVaultClient_ShouldReturnNil_WhenAddrEmpty(t *testing.T) {
	assert.Nil(t, NewVaultClient("", "token"))
}

func TestNewVaultClient_ShouldReturnNil_WhenTokenEmpty(t *testing.T) {
	assert.Nil(t, NewVaultClient("http://vault:8200", ""))
}

func TestNewVaultClient_ShouldReturnClient_WhenBothSet(t *testing.T) {
	c := NewVaultClient("http://vault:8200", "token")
	assert.NotNil(t, c)
}

// ── ProvisionKey ──────────────────────────────────────────────────────────────

func TestVaultClient_ProvisionKey_ShouldSucceed_WhenVaultReturns200(t *testing.T) {
	// Arrange
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "dev-token", r.Header.Get("X-Vault-Token"))
			w.WriteHeader(http.StatusOK)
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.ProvisionKey(context.Background(), "sub-uuid")

	// Assert
	assert.NoError(t, err)
}

func TestVaultClient_ProvisionKey_ShouldSucceed_WhenKeyAlreadyExists(t *testing.T) {
	// Arrange — Vault returns 400 for duplicate key creation; treated as success (idempotent).
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.ProvisionKey(context.Background(), "sub-uuid")

	// Assert — 400 from Vault = key already exists = idempotent success
	assert.NoError(t, err)
}

func TestVaultClient_ProvisionKey_ShouldReturnError_WhenVaultReturns500(t *testing.T) {
	// Arrange
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.ProvisionKey(context.Background(), "sub-uuid")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

// ── DeleteKey ─────────────────────────────────────────────────────────────────

func TestVaultClient_DeleteKey_ShouldSucceed_WhenBothStepsSucceed(t *testing.T) {
	// Arrange
	configCalled := false
	deleteCalled := false
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid/config": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			configCalled = true
			w.WriteHeader(http.StatusNoContent)
		},
		"/v1/transit/keys/user-sub-uuid": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				deleteCalled = true
				w.WriteHeader(http.StatusNoContent)
			}
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.DeleteKey(context.Background(), "sub-uuid")

	// Assert
	assert.NoError(t, err)
	assert.True(t, configCalled, "config endpoint must be called to enable deletion")
	assert.True(t, deleteCalled, "delete endpoint must be called")
}

func TestVaultClient_DeleteKey_ShouldReturnError_WhenConfigStepFails(t *testing.T) {
	// Arrange
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid/config": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.DeleteKey(context.Background(), "sub-uuid")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "enable deletion")
}

func TestVaultClient_DeleteKey_ShouldReturnError_WhenDeleteStepFails(t *testing.T) {
	// Arrange
	srv := fakeVault(map[string]http.HandlerFunc{
		"/v1/transit/keys/user-sub-uuid/config": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
		"/v1/transit/keys/user-sub-uuid": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	defer srv.Close()
	client := NewVaultClient(srv.URL, "dev-token")

	// Act
	err := client.DeleteKey(context.Background(), "sub-uuid")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "delete key")
}

// ── userKeyName ───────────────────────────────────────────────────────────────

func TestUserKeyName_ShouldReturnUserPrefixedName(t *testing.T) {
	assert.Equal(t, "user-550e8400-e29b-41d4-a716-446655440000", userKeyName("550e8400-e29b-41d4-a716-446655440000"))
}
