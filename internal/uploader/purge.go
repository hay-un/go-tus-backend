package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ProcessPurgeEvent handles a bucket.purge Kafka event published by go-shares.
// It hard-deletes all MinIO objects + the bucket, cleans up shares,
// then removes the deleted_buckets DB record via go-shares.
func (a *App) ProcessPurgeEvent(ctx context.Context, data []byte) {
	var event struct {
		BucketName  string `json:"bucketName"`
		OwnerUserID string `json:"ownerUserId"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("purge consumer: failed to parse event: %v", err)
		return
	}
	if event.BucketName == "" {
		log.Printf("purge consumer: missing bucketName in event")
		return
	}

	log.Printf("purge consumer: purging bucket %q (owner: %s)", event.BucketName, event.OwnerUserID)

	// List all objects (may be empty if bucket was already gone)
	listOut, listErr := a.S3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(event.BucketName),
	})
	if listErr != nil && !isBucketNotFound(listErr) {
		log.Printf("purge consumer: failed to list objects in %q: %v", event.BucketName, listErr)
		return
	}

	// Delete all objects
	if listOut != nil && len(listOut.Contents) > 0 {
		objectIDs := make([]types.ObjectIdentifier, 0, len(listOut.Contents))
		for _, obj := range listOut.Contents {
			objectIDs = append(objectIDs, types.ObjectIdentifier{Key: obj.Key})
		}
		if _, err := a.S3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(event.BucketName),
			Delete: &types.Delete{Objects: objectIDs},
		}); err != nil {
			log.Printf("purge consumer: failed to delete objects in %q: %v", event.BucketName, err)
			return
		}
	}

	// Delete the MinIO bucket (ignore "already gone" errors)
	if _, err := a.S3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(event.BucketName),
	}); err != nil && !isBucketNotFound(err) {
		log.Printf("purge consumer: failed to delete MinIO bucket %q: %v", event.BucketName, err)
		return
	}

	// Delete all shares for this bucket (non-fatal)
	if a.Shares != nil {
		if err := a.Shares.DeleteSharesForBucket(ctx, event.BucketName); err != nil {
			log.Printf("purge consumer: failed to delete shares for %q: %v", event.BucketName, err)
		}
	}

	// Remove the deleted_buckets DB record (non-fatal — ticker will retry on next run if this fails)
	if a.Shares != nil {
		if err := a.Shares.PurgeBucketRecord(ctx, event.BucketName); err != nil {
			log.Printf("purge consumer: failed to purge DB record for %q: %v", event.BucketName, err)
		}
	}

	// Emit audit event
	go func() {
		if err := a.Audit.Emit(ctx, AuditEvent{
			Action:    "bucket.purge",
			Resource:  fmt.Sprintf("/buckets/%s", event.BucketName),
			UserID:    event.OwnerUserID,
			Method:    "SYSTEM",
			Status:    200,
			Timestamp: time.Now().UTC(),
		}); err != nil {
			log.Printf("purge consumer: audit emit error: %v", err)
		}
	}()

	log.Printf("purge consumer: bucket %q purged successfully", event.BucketName)
}
