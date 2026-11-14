package bundle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
)

const manifest = `{
  "source": "SOURCE_POWER",
  "format": "FORMAT_POWER_JSON",
  "variables": ["T2M"],
  "point": {"lat": 28.61, "lon": 77.21},
  "files": [{"blob": {"key": "power.json"}}]
}`

func writeBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type recordingSink struct {
	mu    sync.Mutex
	files map[string]string
}

func (s *recordingSink) Put(_ context.Context, f core.File) error {
	data, err := io.ReadAll(f.Body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[f.Name] = string(data)
	return nil
}

func task(name string) *ingestv1.IngestTask {
	return &ingestv1.IngestTask{Adapter: &ingestv1.IngestTask_Bundle{Bundle: &ingestv1.Bundle{Name: name}}}
}

func TestFetchCopiesTheListedFiles(t *testing.T) {
	dir := writeBundle(t, map[string]string{
		"delhi/manifest.json": manifest,
		"delhi/power.json":    `{"type":"Feature"}`,
		"delhi/unlisted.txt":  "ignored",
	})
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{files: map[string]string{}}
	got, err := a.Fetch(context.Background(), task("delhi"), sink)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != ingestv1.Source_SOURCE_POWER || got.Format != ingestv1.Format_FORMAT_POWER_JSON || got.Point.GetLat() != 28.61 {
		t.Errorf("metadata: %+v", got)
	}
	if len(sink.files) != 1 || sink.files["power.json"] != `{"type":"Feature"}` {
		t.Errorf("files: %v", sink.files)
	}
}

func TestBadNamesAreRejected(t *testing.T) {
	dir := writeBundle(t, map[string]string{"delhi/manifest.json": manifest, "secret.json": "{}"})
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "missing", "../delhi", "delhi/../..", "/etc"} {
		_, err := a.Fetch(context.Background(), task(name), &recordingSink{files: map[string]string{}})
		e := core.AsError(err)
		if e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST || e.Transient {
			t.Errorf("name %q: got %v transient=%v", name, e.Code, e.Transient)
		}
	}
}

func TestManifestCantReadOutsideTheBundleDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	escaping := `{"format": "FORMAT_USER_CSV", "files": [{"blob": {"key": "../../` + filepath.Base(outside) + `/secret.json"}}]}`
	dir := writeBundle(t, map[string]string{"evil/manifest.json": escaping})
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Fetch(context.Background(), task("evil"), &recordingSink{files: map[string]string{}}); err == nil {
		t.Fatal("a manifest read a file outside the bundle directory")
	}
}

func TestFingerprintChangesWhenAFileChanges(t *testing.T) {
	dir := writeBundle(t, map[string]string{"delhi/manifest.json": manifest, "delhi/power.json": "v1"})
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.Fingerprint(context.Background(), task("delhi"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "delhi/power.json"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := a.Fingerprint(context.Background(), task("delhi"))
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("fingerprint didn't change after editing a file")
	}
}

func TestShippedBundlesAreValid(t *testing.T) {
	const shipped = "../../bundle"
	entries, err := os.ReadDir(shipped)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(shipped)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			sink := &recordingSink{files: map[string]string{}}
			if _, err := a.Fetch(context.Background(), task(e.Name()), sink); err != nil {
				t.Fatal(err)
			}
			if len(sink.files) == 0 {
				t.Error("no files")
			}
		})
	}
}

func TestFetchNarrowsToTheRequestedVariables(t *testing.T) {
	two := `{"format": "FORMAT_POWER_JSON", "variables": ["T2M", "PRECTOTCORR"], "files": [{"blob": {"key": "power.json"}}]}`
	dir := writeBundle(t, map[string]string{"delhi/manifest.json": two, "delhi/power.json": "{}"})
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := func(vars ...string) *ingestv1.IngestTask {
		return &ingestv1.IngestTask{Adapter: &ingestv1.IngestTask_Bundle{Bundle: &ingestv1.Bundle{Name: "delhi", Variables: vars}}}
	}

	got, err := a.Fetch(context.Background(), want("T2M"), &recordingSink{files: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Variables) != 1 || got.Variables[0] != "T2M" {
		t.Errorf("variables: %v", got.Variables)
	}

	_, err = a.Fetch(context.Background(), want("NO2"), &recordingSink{files: map[string]string{}})
	if e := core.AsError(err); e.Code != gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST || e.Transient {
		t.Errorf("unknown variable: got %v transient=%v", e.Code, e.Transient)
	}
}
