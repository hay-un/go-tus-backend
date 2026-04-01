package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockKeycloakGranter is a testify mock for KeycloakGranter.
type MockKeycloakGranter struct {
	mock.Mock
}

func (m *MockKeycloakGranter) GrantBucket(ctx context.Context, email, bucket string) error {
	args := m.Called(ctx, email, bucket)
	return args.Error(0)
}

func (m *MockKeycloakGranter) DeleteUser(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

// ── sanitizeUsername ──────────────────────────────────────────────────────────

func TestSanitizeUsername_ShouldNormalizeInput_WhenVariousFormatsProvided(t *testing.T) {
	// Arrange
	tests := []struct {
		input string
		want  string
	}{
		{"bambang", "bambang"},
		{"Bambang Pratama", "bambang-pratama"},
		{"rosa.maria@example.com", "rosa-maria-example-com"},
		{"UPPERCASE", "uppercase"},
		{"--leading-trailing--", "leading-trailing"},
		{"multiple---hyphens", "multiple-hyphens"},
		{"", ""},
		{"123numbers456", "123numbers456"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			// Act
			got := sanitizeUsername(tt.input)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// ── InternalSecretMiddleware ──────────────────────────────────────────────────

func TestInternalSecretMiddleware_ShouldAllowRequest_WhenSecretIsValid(t *testing.T) {
	// Arrange
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := InternalSecretMiddleware("my-secret", next)
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", nil)
	req.Header.Set("X-Internal-Secret", "my-secret")
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestInternalSecretMiddleware_ShouldReturn401_WhenSecretIsWrong(t *testing.T) {
	// Arrange
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	handler := InternalSecretMiddleware("my-secret", next)
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", nil)
	req.Header.Set("X-Internal-Secret", "wrong-secret")
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestInternalSecretMiddleware_ShouldReturn401_WhenSecretHeaderIsMissing(t *testing.T) {
	// Arrange
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	handler := InternalSecretMiddleware("my-secret", next)
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestInternalSecretMiddleware_ShouldAllowAllRequests_WhenSecretIsEmpty(t *testing.T) {
	// Arrange
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := InternalSecretMiddleware("", next) // empty = dev mode
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", nil)
	rr := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── ProvisionUserHandler ──────────────────────────────────────────────────────

func TestProvisionUserHandler_ShouldReturn201WithCreatedStatus_WhenBucketDoesNotExist(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "bambang-files"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "bambang-files"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"username":"bambang"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp provisionResponse
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "bambang-files", resp.Bucket)
	assert.Equal(t, "created", resp.Status)
	mockS3.AssertExpectations(t)
}

func TestProvisionUserHandler_ShouldReturn200WithExistsStatus_WhenBucketAlreadyExists(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "rosa-files"
	}), mock.Anything).Return(&s3.HeadBucketOutput{}, nil)

	body := `{"username":"rosa"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	var resp provisionResponse
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "rosa-files", resp.Bucket)
	assert.Equal(t, "exists", resp.Status)
	mockS3.AssertNotCalled(t, "CreateBucket")
	mockS3.AssertExpectations(t)
}

func TestProvisionUserHandler_ShouldReturn400_WhenUsernameIsEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	body := `{"username":""}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "must not be empty")
	mockS3.AssertNotCalled(t, "HeadBucket")
	mockS3.AssertNotCalled(t, "CreateBucket")
}

func TestProvisionUserHandler_ShouldNormalizeUsernameAndReturn201_WhenInputHasUppercaseAndDots(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(in *s3.HeadBucketInput) bool {
		return aws.ToString(in.Bucket) == "bambang-pratama-files"
	}), mock.Anything).Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.MatchedBy(func(in *s3.CreateBucketInput) bool {
		return aws.ToString(in.Bucket) == "bambang-pratama-files"
	}), mock.Anything).Return(&s3.CreateBucketOutput{}, nil)

	body := `{"username":"Bambang.Pratama"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	var resp provisionResponse
	assert.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "bambang-pratama-files", resp.Bucket)
	mockS3.AssertExpectations(t)
}

func TestProvisionUserHandler_ShouldReturn405_WhenMethodIsNotPost(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	req, _ := http.NewRequest(http.MethodGet, "/internal/provision-user", nil)
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestProvisionUserHandler_ShouldCallGranter_WhenEmailProvided(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockGranter := new(MockKeycloakGranter)
	app := newTestApp(mockS3)
	app.KeycloakGranter = mockGranter

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})
	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)
	mockGranter.On("GrantBucket", mock.Anything, "bambang@test.com", "bambang-files").
		Return(nil)

	body := `{"username":"bambang","email":"bambang@test.com"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusCreated, rr.Code)
	mockGranter.AssertExpectations(t)
}

func TestProvisionUserHandler_ShouldReturn500_WhenGranterFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockGranter := new(MockKeycloakGranter)
	app := newTestApp(mockS3)
	app.KeycloakGranter = mockGranter

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})
	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.CreateBucketOutput{}, nil)
	mockGranter.On("GrantBucket", mock.Anything, "bambang@test.com", "bambang-files").
		Return(fmt.Errorf("keycloak unavailable"))

	body := `{"username":"bambang","email":"bambang@test.com"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "failed to grant bucket access")
}

func TestProvisionUserHandler_ShouldSkipGranter_WhenEmailIsEmpty(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	mockGranter := new(MockKeycloakGranter)
	app := newTestApp(mockS3)
	app.KeycloakGranter = mockGranter

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(&s3.HeadBucketOutput{}, nil)

	body := `{"username":"bambang"}` // no email field
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	mockGranter.AssertNotCalled(t, "GrantBucket")
}

func TestProvisionUserHandler_ShouldReturn500_WhenBucketExistsCheckFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("s3 internal error"))

	body := `{"username":"bambang"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}

func TestProvisionUserHandler_ShouldReturn500_WhenCreateBucketFails(t *testing.T) {
	// Arrange
	mockS3 := new(MockS3Client)
	app := newTestApp(mockS3)

	mockS3.On("HeadBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &types.NoSuchBucket{})

	mockS3.On("CreateBucket", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("create failed"))

	body := `{"username":"bambang"}`
	req, _ := http.NewRequest(http.MethodPost, "/internal/provision-user", strings.NewReader(body))
	rr := httptest.NewRecorder()

	// Act
	app.ProvisionUserHandler(rr, req)

	// Assert
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockS3.AssertExpectations(t)
}
