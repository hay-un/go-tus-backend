package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// VaultClient interacts with HashiCorp Vault's transit engine to provision and
// delete per-user AES-256-GCM encryption keys for MinIO SSE-KMS.
//
// Key naming convention: "user-{keycloak_sub}" (flat, URL-safe).
// Crypto-shredding: deleting the key makes all MinIO objects encrypted with it
// permanently inaccessible, fulfilling GDPR Art. 17 right to erasure.
type VaultClient struct {
	addr  string
	token string
	http  *http.Client
}

// NewVaultClient creates a VaultClient. Returns nil if addr or token is empty
// (SSE-KMS disabled — dev mode without Vault).
func NewVaultClient(addr, token string) *VaultClient {
	if addr == "" || token == "" {
		return nil
	}
	return &VaultClient{addr: addr, token: token, http: &http.Client{}}
}

// userKeyName returns the Vault transit key name for a given Keycloak subject UUID.
func userKeyName(sub string) string {
	return "user-" + sub
}

// ProvisionKey creates the transit key for a user if it does not already exist.
// Idempotent: if the key already exists, the call succeeds silently.
// Best-effort: callers should log but not block on errors.
func (v *VaultClient) ProvisionKey(ctx context.Context, sub string) error {
	name := userKeyName(sub)
	url := fmt.Sprintf("%s/v1/transit/keys/%s", v.addr, name)

	body, _ := json.Marshal(map[string]string{"type": "aes256-gcm96"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 200/204 = created; 400 = key already exists — both are success.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("vault: provision key %q: unexpected status %d", name, resp.StatusCode)
}

// DeleteKey permanently deletes the user's encryption key from Vault.
// This is crypto-shredding (GDPR Art. 17): any MinIO data encrypted with this
// key becomes permanently inaccessible, even if the ciphertext remains on disk.
//
// Two-step process required by Vault: first enable deletion, then delete.
// Best-effort: callers should log but not block on errors.
func (v *VaultClient) DeleteKey(ctx context.Context, sub string) error {
	name := userKeyName(sub)

	// Step 1: allow deletion (Vault transit keys are deletion-protected by default).
	configURL := fmt.Sprintf("%s/v1/transit/keys/%s/config", v.addr, name)
	configBody, _ := json.Marshal(map[string]bool{"deletion_allowed": true})
	configReq, err := http.NewRequestWithContext(ctx, http.MethodPost, configURL, bytes.NewReader(configBody))
	if err != nil {
		return err
	}
	configReq.Header.Set("X-Vault-Token", v.token)
	configReq.Header.Set("Content-Type", "application/json")

	configResp, err := v.http.Do(configReq)
	if err != nil {
		return err
	}
	configResp.Body.Close()
	if configResp.StatusCode != http.StatusNoContent && configResp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault: enable deletion for key %q: status %d", name, configResp.StatusCode)
	}

	// Step 2: delete the key.
	deleteURL := fmt.Sprintf("%s/v1/transit/keys/%s", v.addr, name)
	deleteReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}
	deleteReq.Header.Set("X-Vault-Token", v.token)

	deleteResp, err := v.http.Do(deleteReq)
	if err != nil {
		return err
	}
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusNoContent && deleteResp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault: delete key %q: status %d", name, deleteResp.StatusCode)
	}
	return nil
}
