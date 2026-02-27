package uploader

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// InternalSecretMiddleware guards internal endpoints with a shared secret.
// If secret is empty (dev mode), all requests are allowed.
func InternalSecretMiddleware(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret != "" && r.Header.Get("X-Internal-Secret") != secret {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUsername lowercases the input, replaces runs of non-alphanumeric
// characters with a single hyphen, and trims leading/trailing hyphens.
func sanitizeUsername(username string) string {
	s := strings.ToLower(username)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

type provisionResponse struct {
	Bucket string `json:"bucket"`
	Status string `json:"status"`
}

// ProvisionUserHandler handles POST /internal/provision-user.
// Creates the user's personal bucket ({username}-files) if it does not already exist.
// Idempotent: returns 200 if the bucket already exists, 201 if newly created.
func (a *App) ProvisionUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	sanitized := sanitizeUsername(body.Username)
	if sanitized == "" {
		jsonError(w, "username must not be empty", http.StatusBadRequest)
		return
	}

	bucketName := sanitized + "-files"

	exists, err := a.bucketExists(r, bucketName)
	if err != nil {
		jsonError(w, fmt.Sprintf("failed to check bucket: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if exists {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(provisionResponse{Bucket: bucketName, Status: "exists"}) //nolint:errcheck
		return
	}

	if _, err := a.S3Client.CreateBucket(r.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}); err != nil {
		jsonError(w, fmt.Sprintf("failed to create bucket: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(provisionResponse{Bucket: bucketName, Status: "created"}) //nolint:errcheck
	emitAudit(a, r, "bucket.provision", "/internal/provision-user/"+bucketName, http.StatusCreated)
}
