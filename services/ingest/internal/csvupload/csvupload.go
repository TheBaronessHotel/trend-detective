// Package csvupload is the csv-upload adapter. It reads a CSV the user
// uploaded through CreateUploadURL, checks it, and stores a cleaned-up copy
// with two columns, date and value, as FORMAT_USER_CSV.
//
// Every problem with the file goes back to the user with the row number and
// what to change, for example `Row 14: "abc" isn't a number.` The adapter
// also finds the time step from the dates, because the catalog can't know
// it, and the dataset records it for preprocess.
package csvupload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/blob"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
)

// version changes when the adapter starts writing different files for the
// same upload, which invalidates cached datasets.
const version = "csv-upload/1"

// The bucket CreateUploadURL signs uploads for. Tasks pointing anywhere
// else are refused.
const uploadsBucket = "uploads"

// A trend needs at least this much data.
const (
	minMonths = 24
	minValues = 12
)

// Uploads is the uploads bucket.
type Uploads interface {
	Stat(ctx context.Context, key string) (blob.Info, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Adapter implements core.Adapter for SOURCE_CSV_UPLOAD.
type Adapter struct {
	uploads  Uploads
	maxBytes int64
}

// New returns an Adapter. maxBytes is MAX_UPLOAD_MB, the same limit the
// gateway signs uploads for. A signed PUT can't enforce it, so the adapter
// checks it again.
func New(uploads Uploads, maxBytes int64) *Adapter {
	return &Adapter{uploads: uploads, maxBytes: maxBytes}
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return ingestv1.Source_SOURCE_CSV_UPLOAD }

// Fingerprint implements core.Adapter. It includes the upload's ETag,
// because the signed URL can be used again to replace the file under the
// same key.
func (a *Adapter) Fingerprint(ctx context.Context, task *ingestv1.IngestTask) (string, error) {
	key, err := uploadKey(task)
	if err != nil {
		return "", err
	}
	info, err := a.stat(ctx, key)
	if err != nil {
		return "", err
	}
	return version + "/" + info.ETag, nil
}

// Fetch implements core.Adapter.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	key, err := uploadKey(task)
	if err != nil {
		return nil, err
	}
	info, err := a.stat(ctx, key)
	if err != nil {
		return nil, err
	}
	if info.Size > a.maxBytes {
		return nil, a.tooLarge(info.Size)
	}
	data, err := a.read(ctx, key)
	if err != nil {
		return nil, err
	}

	t, err := parse(data)
	if err != nil {
		return nil, userError(err)
	}
	if err := t.trim(task.GetDates()); err != nil {
		return nil, userError(err)
	}
	if err := t.longEnough(); err != nil {
		return nil, userError(err)
	}

	out := t.write()
	err = sink.Put(ctx, core.File{
		Name:        "user.csv",
		Body:        bytes.NewReader(out),
		Size:        int64(len(out)),
		ContentType: "text/csv",
		Coverage:    t.coverage(),
	})
	if err != nil {
		return nil, err
	}
	return &core.Fetched{
		Source:    ingestv1.Source_SOURCE_CSV_UPLOAD,
		Format:    ingestv1.Format_FORMAT_USER_CSV,
		Variables: []string{t.valueName},
		Step:      t.step,
	}, nil
}

func uploadKey(task *ingestv1.IngestTask) (string, error) {
	up := task.GetCsvUpload().GetUpload()
	if up.GetKey() == "" || up.GetBucket() != uploadsBucket {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "csv-upload task needs a key in the %s bucket, got %q in %q", uploadsBucket, up.GetKey(), up.GetBucket())
	}
	return up.GetKey(), nil
}

func (a *Adapter) stat(ctx context.Context, key string) (blob.Info, error) {
	info, err := a.uploads.Stat(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return blob.Info{}, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The uploaded file wasn't found. Upload it again.", "upload %s not found", key)
	}
	if err != nil {
		return blob.Info{}, core.Transient(err)
	}
	return info, nil
}

// read reads the upload, stopping one byte past the limit, since the file
// can change between Stat and Get.
func (a *Adapter) read(ctx context.Context, key string) ([]byte, error) {
	r, err := a.uploads.Get(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The uploaded file wasn't found. Upload it again.", "upload %s not found", key)
	}
	if err != nil {
		return nil, core.Transient(err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, a.maxBytes+1))
	if err != nil {
		return nil, core.Transient(fmt.Errorf("read upload %s: %w", key, err))
	}
	if int64(len(data)) > a.maxBytes {
		return nil, a.tooLarge(int64(len(data)))
	}
	return data, nil
}

func (a *Adapter) tooLarge(size int64) error {
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE, false,
		fmt.Sprintf("The file is larger than the %d MB limit.", a.maxBytes>>20), "upload is at least %d bytes", size)
}

// userError turns a problem with the file into an error the user sees.
func userError(err error) error {
	var fe *fileError
	if errors.As(err, &fe) {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false, fe.msg, "bad upload: %s", fe.msg)
	}
	var nd *noDataError
	if errors.As(err, &nd) {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false, nd.msg, "upload: %s", nd.msg)
	}
	return err
}

// noDataError means the file is fine but has nothing in the job's dates.
type noDataError struct{ msg string }

func (e *noDataError) Error() string { return e.msg }

// trim keeps the rows inside the job's dates.
func (t *table) trim(dates *commonv1.DateRange) error {
	start, end := toTime(dates.GetStart()), toTime(dates.GetEnd())
	if start.IsZero() || end.IsZero() {
		return nil
	}
	if t.step == commonv1.TimeStep_TIME_STEP_MONTHLY {
		start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	first, last := t.rows[0].date, t.rows[len(t.rows)-1].date
	kept := t.rows[:0]
	for _, r := range t.rows {
		if !r.date.Before(start) && !r.date.After(end) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return &noDataError{msg: fmt.Sprintf("The file covers %s to %s, which is outside the dates you picked.",
			label(first, t.step), label(last, t.step))}
	}
	t.rows = kept
	return nil
}

// longEnough checks there's enough data to look for a trend.
func (t *table) longEnough() error {
	first, last := t.rows[0].date, t.rows[len(t.rows)-1].date
	months := (last.Year()-first.Year())*12 + int(last.Month()-first.Month()) + 1
	if months < minMonths {
		return fileErrorf("The data covers %s to %s. Finding a trend needs at least two years.",
			label(first, t.step), label(last, t.step))
	}
	values := 0
	for _, r := range t.rows {
		if r.ok {
			values++
		}
	}
	if values < minValues {
		return fileErrorf("Only %d rows have a value. Finding a trend needs at least %d.", values, minValues)
	}
	return nil
}

// coverage is the dates the cleaned-up rows span. A monthly row covers its
// whole month and a weekly row its week.
func (t *table) coverage() *commonv1.DateRange {
	first, last := t.rows[0].date, t.rows[len(t.rows)-1].date
	switch t.step {
	case commonv1.TimeStep_TIME_STEP_MONTHLY:
		last = last.AddDate(0, 1, -1)
	case commonv1.TimeStep_TIME_STEP_WEEKLY:
		last = last.AddDate(0, 0, 6)
	}
	return &commonv1.DateRange{Start: toDate(first), End: toDate(last)}
}

func toTime(d *commonv1.Date) time.Time {
	if d == nil {
		return time.Time{}
	}
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
}

func toDate(t time.Time) *commonv1.Date {
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}
