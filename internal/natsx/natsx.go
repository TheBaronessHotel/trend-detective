// Package natsx publishes and reads the protojson messages that travel over
// JetStream, and carries the trace context in NATS headers.
//
// Header keys stay lowercase. OpenTelemetry's HeaderCarrier canonicalizes
// "traceparent" to "Traceparent", and the Python propagator then can't find
// it, which splits one job into two unrelated traces.
package natsx

import (
	"context"
	"fmt"

	"github.com/lein3000zzz/the-watchers/pkg/observability/tracing/oteltracing"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Subjects the services publish on. The TASKS and EVENTS streams cover them.
const (
	SubjectIngest     = "tasks.ingest"
	SubjectPreprocess = "tasks.preprocess"
	SubjectAnalysis   = "tasks.analysis"
	SubjectStageDone  = "events.stage.done"
	// TaskProgress, while a task runs. Best effort; StageDone is what counts.
	SubjectTaskProgress = "events.task.progress"
)

// Publish sends msg as protojson. msgID goes in Nats-Msg-Id, so JetStream
// drops a second publish with the same ID inside its duplicate window.
func Publish(ctx context.Context, js jetstream.JetStream, subject, msgID string, msg proto.Message) error {
	data, err := protojson.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode %s: %w", subject, err)
	}
	m := nats.NewMsg(subject)
	m.Data = data
	m.Header[jetstream.MsgIDHeader] = []string{msgID}
	InjectTrace(ctx, m.Header)
	if _, err := js.PublishMsg(ctx, m); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// Decode reads protojson into msg. Unknown fields are ignored, so an older
// service can read messages from a newer one.
func Decode(data []byte, msg proto.Message) error {
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, msg)
}

// InjectTrace writes the trace context from ctx into h, with lowercase keys.
func InjectTrace(ctx context.Context, h nats.Header) {
	carrier := map[string]string{}
	oteltracing.InjectMap(ctx, carrier)
	for k, v := range carrier {
		// Assigning to the map directly keeps the key exactly as written.
		h[k] = []string{v}
	}
}

// ExtractTrace returns ctx with the trace context found in h, if any.
func ExtractTrace(ctx context.Context, h nats.Header) context.Context {
	carrier := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			carrier[k] = v[0]
		}
	}
	return oteltracing.ExtractMap(ctx, carrier)
}

// Publisher publishes protojson messages on JetStream.
type Publisher struct {
	JS jetstream.JetStream
}

// Publish calls the package-level Publish.
func (p Publisher) Publish(ctx context.Context, subject, msgID string, msg proto.Message) error {
	return Publish(ctx, p.JS, subject, msgID, msg)
}
