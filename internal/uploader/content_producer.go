package uploader

import (
	"context"
	"encoding/json"
	"time"

	"github.com/segmentio/kafka-go"
)

// ContentEvent is the payload written to the Kafka content-indexing topic.
type ContentEvent struct {
	UserID      string    `json:"userId"`
	Bucket      string    `json:"bucket"`
	Key         string    `json:"key"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	Action      string    `json:"action"` // "index" | "delete"
	Timestamp   time.Time `json:"timestamp"`
}

// ContentProducer is the interface for emitting content indexing events.
// It is satisfied by KafkaContentProducer (production) and NoopContentProducer (dev/test).
type ContentProducer interface {
	EmitContent(ctx context.Context, event ContentEvent) error
	Close() error
}

// ─── Kafka implementation ────────────────────────────────────────────────────

// KafkaContentProducer sends ContentEvents as JSON messages to a Kafka topic.
// Uses the KafkaWriter interface (defined in audit.go) for testability.
type KafkaContentProducer struct {
	writer KafkaWriter
}

// NewKafkaContentProducer creates a KafkaContentProducer that writes to the given brokers and topic.
func NewKafkaContentProducer(brokers []string, topic string) *KafkaContentProducer {
	return &KafkaContentProducer{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokers...),
			Topic:    topic,
			Balancer: &kafka.LeastBytes{},
		},
	}
}

// NewKafkaContentProducerWithWriter creates a KafkaContentProducer with a custom KafkaWriter.
// Intended for use in tests to inject a mock writer.
func NewKafkaContentProducerWithWriter(w KafkaWriter) *KafkaContentProducer {
	return &KafkaContentProducer{writer: w}
}

// EmitContent serialises event to JSON and writes it to Kafka.
func (p *KafkaContentProducer) EmitContent(ctx context.Context, event ContentEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Value: payload})
}

// Close closes the underlying Kafka writer.
func (p *KafkaContentProducer) Close() error {
	return p.writer.Close()
}

// ─── Noop implementation ─────────────────────────────────────────────────────

// NoopContentProducer discards all events. Used when KAFKA_BROKERS is not set or in tests.
type NoopContentProducer struct{}

// EmitContent is a no-op; it always returns nil.
func (p *NoopContentProducer) EmitContent(_ context.Context, _ ContentEvent) error { return nil }

// Close is a no-op.
func (p *NoopContentProducer) Close() error { return nil }
