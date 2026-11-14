package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, body io.Reader, _ int64, ct string) (*commonv1.BlobRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return &commonv1.BlobRef{Bucket: "raw", Key: key, SizeBytes: int64(len(data)), ContentType: ct}, nil
}

func (m *memStore) PutProto(ctx context.Context, key string, msg proto.Message) (*commonv1.BlobRef, error) {
	data, err := protojson.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return m.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/json")
}

func (m *memStore) GetProto(_ context.Context, key string, msg proto.Message) error {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return blob.ErrNotFound
	}
	return protojson.Unmarshal(data, msg)
}

type fakeAdapter struct {
	files []File
	err   error
	calls int
}

func (f *fakeAdapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_BUNDLE }

func (f *fakeAdapter) Fingerprint(context.Context, *ingestv1.IngestTask) (string, error) {
	return "fake/1", nil
}

func (f *fakeAdapter) Fetch(ctx context.Context, _ *ingestv1.IngestTask, sink Sink) (*Fetched, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	for _, file := range f.files {
		if err := sink.Put(ctx, file); err != nil {
			return nil, err
		}
	}
	return &Fetched{Source: ingestv1.Source_SOURCE_POWER, Format: ingestv1.Format_FORMAT_POWER_JSON, Variables: []string{"T2M"}}, nil
}

func bundleTask() *ingestv1.IngestTask {
	return &ingestv1.IngestTask{
		VariableId: "power-t2m",
		Bbox:       &commonv1.BBox{West: 77, South: 28, East: 78, North: 29},
		Adapter:    &ingestv1.IngestTask_Bundle{Bundle: &ingestv1.Bundle{Name: "demo"}},
	}
}

func TestIngestWritesFilesAndThenHitsTheCache(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{files: []File{
		{Name: "b.json", Body: strings.NewReader("{}"), Size: 2},
		{Name: "a.json", Body: strings.NewReader("{}"), Size: 2},
	}}
	svc := New(store, adapter)

	ds, hit, err := svc.Ingest(context.Background(), bundleTask())
	if err != nil || hit {
		t.Fatalf("first run: hit=%v err=%v", hit, err)
	}
	if ds.GetSource() != ingestv1.Source_SOURCE_POWER || ds.GetVariableId() != "power-t2m" {
		t.Errorf("dataset metadata: %v", ds)
	}
	if len(ds.GetFiles()) != 2 || !strings.HasSuffix(ds.GetFiles()[0].GetBlob().GetKey(), "/files/a.json") {
		t.Fatalf("want files sorted by key, got %v", ds.GetFiles())
	}
	if !strings.HasPrefix(ds.GetFiles()[0].GetBlob().GetKey(), "bundle/") {
		t.Errorf("want keys under the adapter name, got %s", ds.GetFiles()[0].GetBlob().GetKey())
	}

	again, hit, err := svc.Ingest(context.Background(), bundleTask())
	if err != nil || !hit {
		t.Fatalf("second run: hit=%v err=%v", hit, err)
	}
	if adapter.calls != 1 {
		t.Errorf("adapter ran %d times, want 1", adapter.calls)
	}
	if !proto.Equal(again, ds) {
		t.Errorf("cached dataset differs:\n%v\n%v", again, ds)
	}
}

func TestIngestErrors(t *testing.T) {
	tests := []struct {
		name          string
		adapter       *fakeAdapter
		task          *ingestv1.IngestTask
		wantCode      gatewayv1.ErrorCode
		wantTransient bool
	}{
		{
			name:     "no adapter for the source",
			adapter:  &fakeAdapter{},
			task:     &ingestv1.IngestTask{Adapter: &ingestv1.IngestTask_Power{Power: &ingestv1.Power{}}},
			wantCode: gatewayv1.ErrorCode_ERROR_CODE_UNSUPPORTED,
		},
		{
			name:     "adapter wrote nothing",
			adapter:  &fakeAdapter{},
			task:     bundleTask(),
			wantCode: gatewayv1.ErrorCode_ERROR_CODE_NO_DATA,
		},
		{
			name:          "unexpected adapter error is transient",
			adapter:       &fakeAdapter{err: errors.New("connection reset")},
			task:          bundleTask(),
			wantCode:      gatewayv1.ErrorCode_ERROR_CODE_INTERNAL,
			wantTransient: true,
		},
		{
			name:     "file name with a slash",
			adapter:  &fakeAdapter{files: []File{{Name: "../x", Body: strings.NewReader("")}}},
			task:     bundleTask(),
			wantCode: gatewayv1.ErrorCode_ERROR_CODE_INTERNAL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := New(newMemStore(), tt.adapter).Ingest(context.Background(), tt.task)
			if err == nil {
				t.Fatal("want an error")
			}
			e := AsError(err)
			if e.Code != tt.wantCode || e.Transient != tt.wantTransient {
				t.Errorf("got %v transient=%v, want %v transient=%v", e.Code, e.Transient, tt.wantCode, tt.wantTransient)
			}
			if e.TaskError().GetUserMessage() == "" {
				t.Error("TaskError has no user message")
			}
		})
	}
}

func TestCacheKey(t *testing.T) {
	base, err := CacheKey(bundleTask(), "fp")
	if err != nil {
		t.Fatal(err)
	}

	withToken := bundleTask()
	withToken.EarthdataTokenKey = "earthdata:job-123"
	if k, _ := CacheKey(withToken, "fp"); k != base {
		t.Error("the Earthdata token key changed the cache key")
	}

	otherBox := bundleTask()
	otherBox.Bbox.West = 76
	if k, _ := CacheKey(otherBox, "fp"); k == base {
		t.Error("a different rectangle gave the same cache key")
	}

	if k, _ := CacheKey(bundleTask(), "fp2"); k == base {
		t.Error("a different fingerprint gave the same cache key")
	}
}
