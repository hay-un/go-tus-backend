package uploader

import (
	"context"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// DeleteAccountHandler handles DELETE /users/me.
// It permanently deletes the authenticated user's account:
//  1. Hard-deletes all owned MinIO buckets and their contents
//  2. Removes all share records (owner-side and sharee-side) via go-shares
//  3. Deletes the Keycloak user account
//  4. Emits an account.delete audit event
//
// Deletion is best-effort: each step logs failures but continues so that a transient
// downstream error never leaves the user in a half-deleted state that they cannot retry.
// Data deletion takes priority — Keycloak is deleted last.
func (a *App) DeleteAccountHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	claims, hasClaims := ClaimsFromContext(r.Context())
	if !hasClaims || claims == nil {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	// Step 1: Hard-delete all owned MinIO buckets.
	// AllowedBuckets may be stale — sub-buckets created after the last token refresh
	// are not yet in the JWT. We augment with a live MinIO scan so that orphaned
	// sub-buckets (e.g. parent--child created seconds before account delete) are
	// also cleaned up.
	bucketsToDelete := make(map[string]struct{})
	for _, b := range claims.AllowedBuckets {
		if b != "*" {
			bucketsToDelete[b] = struct{}{}
		}
	}

	if claims.Role != "admin" { // admin wildcard matches every bucket — skip scan to avoid wiping all of MinIO
		listOut, listErr := a.S3Client.ListBuckets(ctx, &s3.ListBucketsInput{})
		if listErr != nil {
			log.Printf("account delete: ListBuckets for stale-JWT discovery: %v (JWT-only fallback)", listErr)
		} else {
			for _, b := range listOut.Buckets {
				if name := aws.ToString(b.Name); name != "" && claims.OwnsBucket(name) {
					bucketsToDelete[name] = struct{}{}
				}
			}
		}
	}

	// NOTE: v1 known limit — DeleteObjects handles up to 1000 objects per bucket.
	for bucket := range bucketsToDelete {
		if err := a.hardDeleteBucket(ctx, bucket); err != nil {
			log.Printf("account delete: MinIO bucket %q: %v", bucket, err)
		}
	}

	// Step 1.5: Crypto-shred — delete the user's Vault encryption key (GDPR Art. 17).
	// This makes any remaining encrypted data permanently inaccessible, even if
	// the MinIO deletion above failed for some objects.
	if a.VaultClient != nil {
		if err := a.VaultClient.DeleteKey(ctx, claims.Subject); err != nil {
			log.Printf("account delete: Vault key for %s: %v", claims.Subject, err)
		}
	}

	// Step 2: Remove all share records (best-effort).
	if a.Shares != nil {
		a.Shares.DeleteUserShares(ctx, claims.Subject, claims.Email)
	}

	// Step 3: Delete Keycloak account (best-effort — data is already gone).
	if a.KeycloakGranter != nil {
		if err := a.KeycloakGranter.DeleteUser(ctx, claims.Subject); err != nil {
			log.Printf("account delete: Keycloak user %s: %v", claims.Subject, err)
		}
	}

	// Step 4: Audit.
	emitAudit(a, r, "account.delete", "/users/me", http.StatusNoContent)

	w.WriteHeader(http.StatusNoContent)
}

// hardDeleteBucket removes all objects inside a bucket and then deletes the bucket itself.
// It is a no-op if the bucket does not exist.
func (a *App) hardDeleteBucket(ctx context.Context, name string) error {
	paginator := s3.NewListObjectsV2Paginator(a.S3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(name),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			if isBucketNotFound(err) {
				return nil
			}
			return err
		}

		if len(page.Contents) > 0 {
			objectIDs := make([]types.ObjectIdentifier, 0, len(page.Contents))
			for _, obj := range page.Contents {
				objectIDs = append(objectIDs, types.ObjectIdentifier{Key: obj.Key})
			}
			if _, err := a.S3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(name),
				Delete: &types.Delete{Objects: objectIDs},
			}); err != nil {
				return err
			}
		}
	}

	if _, err := a.S3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(name),
	}); err != nil && !isBucketNotFound(err) {
		return err
	}
	return nil
}
