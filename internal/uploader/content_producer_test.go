package uploader_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"codirs/backend/internal/uploader"
)

// ─── NoopContentProducer ─────────────────────────────────────────────────────

func TestNoopContentProducer_ShouldNotError_WhenEmitCalled(t *testing.T) {
	// Arrange
	p := &uploader.NoopContentProducer{}
	event := uploader.ContentEvent{Action: "index", Bucket: "test-bucket", Key: "file-id"}

	// Act
	err := p.EmitContent(context.Background(), event)

	// Assert
	assert.NoError(t, err)
}

func TestNoopContentProducer_ShouldNotError_WhenCloseCalled(t *testing.T) {
	// Arrange
	p := &uploader.NoopContentProducer{}

	// Act & Assert
	assert.NoError(t, p.Close())
}

// ─── KafkaContentProducer ────────────────────────────────────────────────────

func TestKafkaContentProducer_ShouldEmitContentEvent_WhenCalled(t *testing.T) {
	// Arrange
	mock := &mockKafkaWriter{}
	p := uploader.NewKafkaContentProducerWithWriter(mock)
	event := uploader.ContentEvent{
		Bucket:      "user-bucket",
		Key:         "upload-id-123",
		Filename:    "video.mp4",
		Size:        1024,
		ContentType: "video/mp4",
		Action:      "index",
	}

	// Act
	err := p.EmitContent(context.Background(), event)

	// Assert
	require.NoError(t, err)
	require.Len(t, mock.messages, 1)

	var got uploader.ContentEvent
	require.NoError(t, json.Unmarshal(mock.messages[0].Value, &got))
	assert.Equal(t, event.Bucket, got.Bucket)
	assert.Equal(t, event.Key, got.Key)
	assert.Equal(t, event.Action, got.Action)
}

func TestKafkaContentProducer_ShouldReturnError_WhenWriterFails(t *testing.T) {
	// Arrange
	mock := &mockKafkaWriter{err: errors.New("kafka connection refused")}
	p := uploader.NewKafkaContentProducerWithWriter(mock)

	// Act
	err := p.EmitContent(context.Background(), uploader.ContentEvent{Action: "index", Bucket: "b", Key: "k"})

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kafka connection refused")
}
