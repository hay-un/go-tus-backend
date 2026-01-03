package uploader

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/s3store"
)

// S3API defines the interface we need from the AWS S3 SDK.
// tusd s3store.New accepts *s3.Client, but for flexibility we might want to configure the client externally.
// However, s3store.New signature in v2 requires s3store.S3API interface which *s3.Client satisfies.

// NewHandlerFromEnv initializes the handler using environment variables.
func NewHandlerFromEnv() (http.Handler, error) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	endpoint := os.Getenv("S3_ENDPOINT")
	region := os.Getenv("AWS_REGION")
	bucketName := os.Getenv("S3_BUCKET")

	// 1. Configure AWS SDK v2
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if endpoint != "" {
			return aws.Endpoint{
				PartitionID:   "aws",
				URL:           endpoint,
				SigningRegion: region,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolverWithOptions(customResolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %w", err)
	}

	// 2. Create S3 Client
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	return NewHandler(bucketName, s3Client)
}

// NewHandler creates a Tus handler with a provided S3 client.
// This allows injecting a mock client or a pre-configured client for testing.
func NewHandler(bucketName string, s3Client s3store.S3API) (http.Handler, error) {
	// 3. Create S3 Store
	store := s3store.New(bucketName, s3Client)

	// 4. Create Tus Handler
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	tusHandler, err := handler.NewHandler(handler.Config{
		BasePath:              "/files/",
		StoreComposer:         composer,
		NotifyCompleteUploads: false,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create tus handler: %w", err)
	}

	// 5. Return the http.Handler
	// We handle the prefix stripping here to ensure consistency
	return http.StripPrefix("/files/", tusHandler), nil
}
