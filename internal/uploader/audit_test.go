package uploader_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"music-streaming/backend/internal/uploader"
)

// ─── NoopAuditProducer ───────────────────────────────────────────────────────

func TestNoopAuditProducer_ShouldReturnNil_WhenEmitCalled(t *testing.T) {
	// Arrange
	p := &uploader.NoopAuditProducer{}
	event := uploader.AuditEvent{Action: "file.download", Status: 200}

	// Act
	err := p.Emit(context.Background(), event)

	// Assert
	assert.NoError(t, err)
}

func TestNoopAuditProducer_ShouldReturnNil_WhenCloseCalled(t *testing.T) {
	// Arrange
	p := &uploader.NoopAuditProducer{}

	// Act & Assert
	assert.NoError(t, p.Close())
}

// ─── KafkaAuditProducer ──────────────────────────────────────────────────────

// mockKafkaWriter captures WriteMessages calls for inspection.
type mockKafkaWriter struct {
	mu       sync.Mutex
	messages []kafka.Message
	err      error
}

func (m *mockKafkaWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *mockKafkaWriter) Close() error { return nil }

func TestKafkaAuditProducer_ShouldMarshalEvent_WhenEmitCalled(t *testing.T) {
	// Arrange
	mock := &mockKafkaWriter{}
	p := uploader.NewKafkaAuditProducerWithWriter(mock)
	event := uploader.AuditEvent{
		UserID:    "user-uuid",
		UserEmail: "user@test.com",
		Action:    "file.download",
		Resource:  "/files/bucket/key",
		Method:    "GET",
		Status:    200,
		IPAddress: "127.0.0.1",
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	// Act
	err := p.Emit(context.Background(), event)

	// Assert
	require.NoError(t, err)
	require.Len(t, mock.messages, 1)

	var got uploader.AuditEvent
	require.NoError(t, json.Unmarshal(mock.messages[0].Value, &got))
	assert.Equal(t, event.Action, got.Action)
	assert.Equal(t, event.UserID, got.UserID)
	assert.Equal(t, event.Status, got.Status)
}

func TestKafkaAuditProducer_ShouldReturnError_WhenWriteFails(t *testing.T) {
	// Arrange
	mock := &mockKafkaWriter{err: errors.New("kafka connection refused")}
	p := uploader.NewKafkaAuditProducerWithWriter(mock)

	// Act
	err := p.Emit(context.Background(), uploader.AuditEvent{Action: "file.download"})

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kafka connection refused")
}

// ─── emitAudit (fire-and-forget) ─────────────────────────────────────────────

func TestEmitAudit_ShouldNotBlock_WhenKafkaDown(t *testing.T) {
	// Arrange: a slow writer that blocks for 1 second
	slowWriter := &slowKafkaWriter{delay: time.Second}
	p := uploader.NewKafkaAuditProducerWithWriter(slowWriter)
	app := &uploader.App{Audit: p, Content: &uploader.NoopContentProducer{}}

	r := httptest.NewRequest("GET", "/files/bucket/key", nil)

	start := time.Now()

	// Act — emitAudit should return immediately
	uploader.EmitAuditForTest(app, r, "file.download", "/files/bucket/key", 200)

	elapsed := time.Since(start)

	// Assert — must complete well under 200ms even though writer takes 1s
	assert.Less(t, elapsed, 200*time.Millisecond, "emitAudit must be fire-and-forget")
}

// slowKafkaWriter simulates a blocked Kafka write.
type slowKafkaWriter struct {
	delay time.Duration
}

func (s *slowKafkaWriter) WriteMessages(_ context.Context, _ ...kafka.Message) error {
	time.Sleep(s.delay)
	return errors.New("kafka down")
}

func (s *slowKafkaWriter) Close() error { return nil }
