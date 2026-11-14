// Package bundle serves datasets shipped with the app, for offline demos and
// the demo video. Each dataset is a folder under the bundle directory, with
// a manifest.json and the files it lists.
//
// The manifest is a RawDataset in protojson. Its files list names relative to
// the folder in blob.key, and every other blob field is ignored. Its source
// is where the files were originally downloaded from.
package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"google.golang.org/protobuf/encoding/protojson"
)

// version changes when the adapter starts writing different files for the
// same bundle.
const version = "bundle/1"

const manifestName = "manifest.json"

// Adapter reads bundles from a directory.
type Adapter struct {
	root *os.Root
}

// New opens the bundle directory. Paths can't escape it.
func New(dir string) (*Adapter, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open bundle directory: %w", err)
	}
	return &Adapter{root: root}, nil
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_BUNDLE }

// Fingerprint hashes the manifest and every file it lists, so editing a
// bundle invalidates its cached copy.
func (a *Adapter) Fingerprint(_ context.Context, task *ingestv1.IngestTask) (string, error) {
	name, m, err := a.manifest(task)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range append([]string{manifestName}, fileNames(m)...) {
		f, err := a.root.Open(filepath.Join(name, rel))
		if err != nil {
			return "", badBundle(name, err)
		}
		fmt.Fprintf(h, "%s\n", rel)
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return "", core.Transient(err)
		}
	}
	return version + "/" + hex.EncodeToString(h.Sum(nil)), nil
}

// Fetch implements core.Adapter.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	name, m, err := a.manifest(task)
	if err != nil {
		return nil, err
	}
	variables := m.GetVariables()
	if want := task.GetBundle().GetVariables(); len(want) > 0 {
		for _, v := range want {
			if !slices.Contains(variables, v) {
				return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
					"The demo dataset doesn't have that variable.", "bundle %q has no variable %q", name, v)
			}
		}
		variables = want
	}
	for _, rf := range m.GetFiles() {
		rel := rf.GetBlob().GetKey()
		f, err := a.root.Open(filepath.Join(name, rel))
		if err != nil {
			return nil, badBundle(name, err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, core.Transient(err)
		}
		err = sink.Put(ctx, core.File{
			Name:        rel,
			Body:        f,
			Size:        info.Size(),
			ContentType: contentType(rel),
			Coverage:    rf.GetCoverage(),
		})
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return &core.Fetched{
		Source:    m.GetSource(),
		Format:    m.GetFormat(),
		Variables: variables,
		Point:     m.GetPoint(),
		Step:      m.GetStep(),
	}, nil
}

func (a *Adapter) manifest(task *ingestv1.IngestTask) (string, *ingestv1.RawDataset, error) {
	name := task.GetBundle().GetName()
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"That demo dataset doesn't exist.", "bad bundle name %q", name)
	}
	data, err := a.root.ReadFile(filepath.Join(name, manifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"That demo dataset doesn't exist.", "no bundle named %q", name)
	}
	if err != nil {
		return "", nil, core.Transient(err)
	}
	m := &ingestv1.RawDataset{}
	if err := protojson.Unmarshal(data, m); err != nil {
		return "", nil, badBundle(name, err)
	}
	if len(m.GetFiles()) == 0 || m.GetFormat() == ingestv1.Format_FORMAT_UNSPECIFIED {
		return "", nil, badBundle(name, errors.New("manifest needs a format and at least one file"))
	}
	return name, m, nil
}

func fileNames(m *ingestv1.RawDataset) []string {
	out := make([]string, 0, len(m.GetFiles()))
	for _, f := range m.GetFiles() {
		out = append(out, f.GetBlob().GetKey())
	}
	return out
}

// badBundle reports a broken bundle. Retrying won't fix it, so it's
// permanent.
func badBundle(name string, err error) error {
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
		"The demo dataset is broken.", "bundle %q: %w", name, err)
}

func contentType(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
