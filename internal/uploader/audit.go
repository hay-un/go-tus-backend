package uploader

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/segmentio/kafka-go"
)

// AuditEvent is the payload written to the Kafka topic and stored in audit_log.
// Field names match docs/API.md Kafka event schema.
type AuditEvent struct {
	UserID    string    `json:"userId"`
	UserEmail string    `json:"userEmail"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Method    string    `json:"method"`
	Status    int       `json:"status"`
	IPAddress string    `json:"ipAddress"`
	UserAgent string    `json:"userAgent"`
	Timestamp time.Time `json:"timestamp"`
}

// AuditProducer is the interface for emitting audit events.
// It is satisfied by KafkaAuditProducer (production) and NoopAuditProducer (dev/test).
type AuditProducer interface {
	Emit(ctx context.Context, event AuditEvent) error
	Close() error
}

// ─── Kafka implementation ────────────────────────────────────────────────────

// KafkaWriter is the subset of kafka.Writer used by KafkaAuditProducer.
// Extracted as an interface to enable mocking in tests.
type KafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// KafkaAuditProducer sends AuditEvents as JSON messages to a Kafka topic.
type KafkaAuditProducer struct {
	writer KafkaWriter
}

// NewKafkaAuditProducer creates a KafkaAuditProducer that writes to the given brokers and topic.
func NewKafkaAuditProducer(brokers []string, topic string) *KafkaAuditProducer {
	return &KafkaAuditProducer{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokers...),
			Topic:    topic,
			Balancer: &kafka.LeastBytes{},
		},
	}
}

// NewKafkaAuditProducerWithWriter creates a KafkaAuditProducer with a custom KafkaWriter.
// Intended for use in tests to inject a mock writer.
func NewKafkaAuditProducerWithWriter(w KafkaWriter) *KafkaAuditProducer {
	return &KafkaAuditProducer{writer: w}
}

// Emit serialises event to JSON and writes it to Kafka.
func (p *KafkaAuditProducer) Emit(ctx context.Context, event AuditEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Value: payload})
}

// Close closes the underlying Kafka writer.
func (p *KafkaAuditProducer) Close() error {
	return p.writer.Close()
}

// ─── Noop implementation ─────────────────────────────────────────────────────

// NoopAuditProducer discards all events. Used when KAFKA_BROKERS is not set.
type NoopAuditProducer struct{}

// Emit is a no-op; it always returns nil.
func (p *NoopAuditProducer) Emit(_ context.Context, _ AuditEvent) error { return nil }

// Close is a no-op.
func (p *NoopAuditProducer) Close() error { return nil }

// ─── Helper ───────────────────────────────────────────────────────────────────

// emitAudit fires an audit event in a background goroutine so the HTTP handler
// never blocks on Kafka. Log errors rather than propagating them to the client.
func emitAudit(a *App, r *http.Request, action, resource string, status int) {
	claims, _ := ClaimsFromContext(r.Context())
	event := AuditEvent{
		Action:    action,
		Resource:  resource,
		Method:    r.Method,
		Status:    status,
		UserAgent: r.UserAgent(),
		IPAddress: realIP(r),
		Timestamp: time.Now().UTC(),
	}
	if claims != nil {
		event.UserID = claims.Subject
		event.UserEmail = claims.Email
	}

	go func() {
		if err := a.Audit.Emit(context.Background(), event); err != nil {
			log.Printf("audit emit error [%s %s]: %v", action, resource, err)
		}
	}()
}

// realIP extracts the client IP from the request, preferring X-Real-IP / X-Forwarded-For.
func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		// May be a comma-separated list; take the first entry.
		for i := 0; i < len(ip); i++ {
			if ip[i] == ',' {
				return ip[:i]
			}
		}
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
