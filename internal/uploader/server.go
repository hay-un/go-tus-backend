package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/s3store"
)

// S3API defines the interface we need from the AWS S3 SDK.
type S3API interface {
	s3store.S3API
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// App holds the dependencies for the uploader service.
type App struct {
	TusHandler *handler.UnroutedHandler
	S3Client   S3API
	BucketName string
	S3Endpoint string
}

// NewAppFromEnv initializes the App using environment variables.
func NewAppFromEnv() (*App, error) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	s3Endpoint := os.Getenv("S3_ENDPOINT")
	region := os.Getenv("AWS_REGION")
	bucketName := os.Getenv("S3_BUCKET")

	// 1. Configure AWS SDK v2
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if s3Endpoint != "" {
			return aws.Endpoint{
				PartitionID:   "aws",
				URL:           s3Endpoint,
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

	tusHandler, err := NewTusHandler(bucketName, s3Client)
	if err != nil {
		return nil, err
	}

	return &App{
		TusHandler: tusHandler,
		S3Client:   s3Client,
		BucketName: bucketName,
		S3Endpoint: s3Endpoint,
	}, nil
}

// NewTusHandler creates a Tus handler with a provided S3 client.
func NewTusHandler(bucketName string, s3Client s3store.S3API) (*handler.UnroutedHandler, error) {
	// 3. Create S3 Store
	store := s3store.New(bucketName, s3Client)

	// 4. Create Tus Handler
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	tusHandler, err := handler.NewUnroutedHandler(handler.Config{
		BasePath:              "/files/",
		StoreComposer:         composer,
		NotifyCompleteUploads: false,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create tus handler: %w", err)
	}

	return tusHandler, nil
}

// ListFilesHandler returns a JSON list of files in the bucket.
func (a *App) ListFilesHandler(w http.ResponseWriter, r *http.Request) {
	output, err := a.S3Client.ListObjectsV2(r.Context(), &s3.ListObjectsV2Input{
		Bucket: aws.String(a.BucketName),
	})
	if err != nil {
		http.Error(w, fmt.Errorf("failed to list objects: %w", err).Error(), http.StatusInternalServerError)
		return
	}

	type FileInfo struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"url"`
	}

	files := make([]FileInfo, 0)
	for _, obj := range output.Contents {
		key := aws.ToString(obj.Key)
		// Only include files (not directories or tus info files ending in .info)
		if key != "" && !((len(key) > 5 && key[len(key)-5:] == ".info")) {
			// Try to get metadata from .info file
			name := key
			infoKey := key + ".info"
			infoObj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
				Bucket: aws.String(a.BucketName),
				Key:    aws.String(infoKey),
			})
			if err == nil {
				var info struct {
					MetaData map[string]string `json:"MetaData"`
				}
				if err := json.NewDecoder(infoObj.Body).Decode(&info); err == nil {
					if filename, ok := info.MetaData["filename"]; ok {
						name = filename
					}
				}
				infoObj.Body.Close()
			}

			// Get the public S3 URL (using localhost for frontend convenience if endpoint is internal)
			url := fmt.Sprintf("%s/%s/%s", a.S3Endpoint, a.BucketName, key)
			// Replace internal minio host with localhost for the browser
			url = strings.Replace(url, "http://minio:9000", "http://localhost:9000", 1)

			files = append(files, FileInfo{
				Key:  key,
				Name: name,
				Size: aws.ToInt64(obj.Size),
				URL:  url,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
