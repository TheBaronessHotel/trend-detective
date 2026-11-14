// Package core is ingest's domain logic. It picks an adapter for a task,
// checks the cache, lets the adapter fetch files into the raw bucket, and
// builds the RawDataset that preprocess reads.
//
// Adapters move bytes only. They never parse scientific formats, which is
// preprocess's job.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/internal/sources"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Adapter fetches raw files for one source.
type Adapter interface {
	// Source is the source this adapter serves.
	Source() ingestv1.Source
	// Fingerprint returns everything besides the task that changes what Fetch
	// writes: the adapter's version, and for the bundle a hash of its files.
	// It goes into the cache key.
	Fingerprint(ctx context.Context, task *ingestv1.IngestTask) (string, error)
	// Fetch writes the task's files to sink and describes what it wrote.
	Fetch(ctx context.Context, task *ingestv1.IngestTask, sink Sink) (*Fetched, error)
}

// Fetched describes what an adapter wrote.
type Fetched struct {
	// Where the data originally comes from. The bundle reports the source its
	// files were downloaded from, so the app shows real provenance.
	Source    ingestv1.Source
	Format    ingestv1.Format
	Variables []string
	// Set for point sources.
	Point *commonv1.LatLon
	// The data's time step, when the adapter found it in the data itself.
	Step commonv1.TimeStep
}

// Sink receives an adapter's files. Put is safe to call from several
// goroutines, so an adapter can download in parallel.
type Sink interface {
	Put(ctx context.Context, f File) error
}

// File is one raw file.
type File struct {
	// Name is unique within the dataset, for example a granule's file name.
	Name string
	Body io.Reader
	// Size in bytes, or -1 when unknown.
	Size        int64
	ContentType string
	// The dates the file covers, when known.
	Coverage *commonv1.DateRange
}

// Store is the raw bucket.
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (*commonv1.BlobRef, error)
	PutProto(ctx context.Context, key string, msg proto.Message) (*commonv1.BlobRef, error)
	// GetProto returns blob.ErrNotFound when the object doesn't exist.
	GetProto(ctx context.Context, key string, msg proto.Message) error
}

// Service runs ingest tasks.
type Service struct {
	store    Store
	adapters map[ingestv1.Source]Adapter
	now      func() time.Time
}

// New returns a Service that writes to store and uses the given adapters.
func New(store Store, adapters ...Adapter) *Service {
	s := &Service{store: store, adapters: map[ingestv1.Source]Adapter{}, now: time.Now}
	for _, a := range adapters {
		s.adapters[a.Source()] = a
	}
	return s
}

// Ingest fetches the task's data, or returns the copy already in the raw
// bucket. cacheHit reports which one happened.
func (s *Service) Ingest(ctx context.Context, task *ingestv1.IngestTask) (ds *ingestv1.RawDataset, cacheHit bool, err error) {
	source := SourceOf(task)
	adapter, ok := s.adapters[source]
	if !ok {
		return nil, false, Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNSUPPORTED, false,
			"This data source isn't available yet.", "no adapter for %s", source)
	}
	fingerprint, err := adapter.Fingerprint(ctx, task)
	if err != nil {
		return nil, false, err
	}
	key, err := CacheKey(task, fingerprint)
	if err != nil {
		return nil, false, err
	}
	prefix := sources.Name(source) + "/" + key
	manifestKey := prefix + "/dataset.json"

	cached := &ingestv1.RawDataset{}
	switch err := s.store.GetProto(ctx, manifestKey, cached); {
	case err == nil:
		return cached, true, nil
	case !errors.Is(err, blob.ErrNotFound):
		return nil, false, Transient(fmt.Errorf("check cache: %w", err))
	}

	sink := &sink{store: s.store, prefix: prefix + "/files/"}
	fetched, err := adapter.Fetch(ctx, task, sink)
	if err != nil {
		return nil, false, err
	}
	files := sink.files()
	if len(files) == 0 {
		return nil, false, Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
			"There's no data for this place and time range.", "adapter %s wrote no files", source)
	}

	ds = &ingestv1.RawDataset{
		Source:     fetched.Source,
		Format:     fetched.Format,
		VariableId: task.GetVariableId(),
		Variables:  fetched.Variables,
		Bbox:       task.GetBbox(),
		Point:      fetched.Point,
		Dates:      task.GetDates(),
		Files:      files,
		FetchedAt:  timestamppb.New(s.now()),
		Step:       fetched.Step,
	}
	if ds.Source == ingestv1.Source_SOURCE_UNSPECIFIED {
		ds.Source = source
	}
	// The manifest goes last, so its presence means the dataset is complete.
	if _, err := s.store.PutProto(ctx, manifestKey, ds); err != nil {
		return nil, false, Transient(fmt.Errorf("write manifest: %w", err))
	}
	return ds, false, nil
}

// SourceOf returns the source a task asks for.
func SourceOf(task *ingestv1.IngestTask) ingestv1.Source {
	switch task.GetAdapter().(type) {
	case *ingestv1.IngestTask_HarmonySubset:
		return ingestv1.Source_SOURCE_HARMONY_SUBSET
	case *ingestv1.IngestTask_GiovanniArea:
		return ingestv1.Source_SOURCE_GIOVANNI_AREA
	case *ingestv1.IngestTask_GiovanniPoint:
		return ingestv1.Source_SOURCE_GIOVANNI_POINT
	case *ingestv1.IngestTask_Power:
		return ingestv1.Source_SOURCE_POWER
	case *ingestv1.IngestTask_CsvUpload:
		return ingestv1.Source_SOURCE_CSV_UPLOAD
	case *ingestv1.IngestTask_Bundle:
		return ingestv1.Source_SOURCE_BUNDLE
	case *ingestv1.IngestTask_Appeears:
		return ingestv1.Source_SOURCE_APPEEARS
	}
	return ingestv1.Source_SOURCE_UNSPECIFIED
}

type sink struct {
	store  Store
	prefix string

	mu  sync.Mutex
	out []*ingestv1.RawFile
}

func (s *sink) Put(ctx context.Context, f File) error {
	if f.Name == "" || path.Base(f.Name) != f.Name {
		// An adapter bug, so retrying won't help.
		return Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
			"Something went wrong while fetching the data.", "file name %q must be a plain name with no slashes", f.Name)
	}
	ref, err := s.store.Put(ctx, s.prefix+f.Name, f.Body, f.Size, f.ContentType)
	if err != nil {
		return Transient(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = append(s.out, &ingestv1.RawFile{Blob: ref, Coverage: f.Coverage})
	return nil
}

// files returns what was written, sorted by key so a dataset always lists its
// files in the same order.
func (s *sink) files() []*ingestv1.RawFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.out)
	slices.SortFunc(out, func(a, b *ingestv1.RawFile) int { return strings.Compare(a.GetBlob().GetKey(), b.GetBlob().GetKey()) })
	return out
}
