// Package harmony is the harmony-subset adapter. It asks NASA Harmony's HOSS
// service for a task's variables, rectangle and dates, and downloads the
// result: one NetCDF-4 file per granule.
//
// A Harmony job can take minutes, so the adapter never waits on one. Each
// delivery of a task moves it one step and returns core.Pending in between:
//
//	no job yet   count granules in CMR, take a slot, submit, check in 10s
//	running      check again in 15s
//	done         download every output file, free the slot
//	failed       report why, free the slot
//
// The Harmony job ID lives in Redis, so a restart or a second task asking for
// the same data picks up the same job instead of starting another one.
package harmony

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/ingest/internal/core"
	"github.com/redis/go-redis/v9"
)

// mode holds what differs between the two Harmony services the adapter
// speaks to.
type mode struct {
	source ingestv1.Source
	// Changes when the adapter starts writing different files for the same
	// task, which invalidates cached datasets.
	version string
	format  ingestv1.Format
	// Ask Giovanni's averaging service for one area-average value per time
	// step, instead of HOSS for a subset of every granule.
	areaAverage bool
}

var (
	subsetMode = mode{source: ingestv1.Source_SOURCE_HARMONY_SUBSET, version: "harmony-subset/1", format: ingestv1.Format_FORMAT_NETCDF4}
	areaMode   = mode{source: ingestv1.Source_SOURCE_GIOVANNI_AREA, version: "giovanni-area/1", format: ingestv1.Format_FORMAT_GIOVANNI_CSV, areaAverage: true}
)

const (
	submitCheck = 10 * time.Second
	pollEvery   = 15 * time.Second
	slotWait    = 30 * time.Second
	// A slot older than this is assumed abandoned, for example by a task
	// that expired, and is freed.
	slotStale = 6 * time.Hour
	stateTTL  = 24 * time.Hour
)

// Config holds the adapter's settings.
type Config struct {
	// For example https://harmony.earthdata.nasa.gov.
	HarmonyURL string
	// For example https://cmr.earthdata.nasa.gov.
	CMRURL string
	// The team's EARTHDATA_TOKEN, used when the user didn't give their own.
	TeamToken string
	// Harmony jobs in flight per token. HARMONY_CONCURRENCY.
	Concurrency int
	// Files downloaded at once per job.
	Downloads int
}

// Adapter implements core.Adapter for SOURCE_HARMONY_SUBSET, or for
// SOURCE_GIOVANNI_AREA when made with NewAreaAverage.
type Adapter struct {
	cfg  Config
	rdb  *redis.Client
	http *http.Client
	mode mode
}

// errLogin means Earthdata sent us to its login page, which happens when the
// token is missing or rejected.
var errLogin = errors.New("redirected to the Earthdata login page")

// New returns the harmony-subset adapter. rdb holds job state, slots and
// user tokens.
func New(cfg Config, rdb *redis.Client) *Adapter {
	return newAdapter(cfg, rdb, subsetMode)
}

// NewAreaAverage returns the giovanni-area adapter. It goes through Harmony
// too, to Giovanni's averaging service, and shares the same slots per token.
// It writes one CSV with one area-average value per time step.
func NewAreaAverage(cfg Config, rdb *redis.Client) *Adapter {
	return newAdapter(cfg, rdb, areaMode)
}

func newAdapter(cfg Config, rdb *redis.Client, m mode) *Adapter {
	return &Adapter{
		cfg:  cfg,
		rdb:  rdb,
		mode: m,
		http: &http.Client{
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if strings.HasSuffix(req.URL.Hostname(), "urs.earthdata.nasa.gov") {
					return errLogin
				}
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

// Source implements core.Adapter.
func (a *Adapter) Source() ingestv1.Source { return a.mode.source }

// Fingerprint implements core.Adapter.
func (a *Adapter) Fingerprint(context.Context, *ingestv1.IngestTask) (string, error) {
	return a.mode.version, nil
}

// Fetch implements core.Adapter. It returns core.Pending until the Harmony
// job is done.
func (a *Adapter) Fetch(ctx context.Context, task *ingestv1.IngestTask, sink core.Sink) (*core.Fetched, error) {
	req, err := newRequest(task, a.mode)
	if err != nil {
		return nil, err
	}
	token, err := a.token(ctx, task)
	if err != nil {
		return nil, err
	}
	stateKey := "td:ingest:harmony:job:" + req.key
	jobID, err := a.rdb.HGet(ctx, stateKey, "job_id").Result()
	if errors.Is(err, redis.Nil) {
		return nil, a.start(ctx, req, token, stateKey)
	}
	if err != nil {
		return nil, core.Transient(err)
	}

	job, err := a.job(ctx, token, jobID)
	if errors.Is(err, errJobGone) {
		// The job expired or never existed. Start over.
		_ = a.rdb.Del(ctx, stateKey).Err()
		return nil, core.Pending(time.Second, "Harmony job "+jobID+" is gone, resubmitting")
	}
	if err != nil {
		return nil, err
	}

	switch job.Status {
	case "successful", "complete_with_errors":
		a.freeSlot(ctx, token, req.key)
		if err := a.download(ctx, token, job, sink); err != nil {
			return nil, err
		}
		_ = a.rdb.Del(ctx, stateKey).Err()
		return &core.Fetched{
			Source:    a.mode.source,
			Format:    a.mode.format,
			Variables: req.variables,
		}, nil
	case "failed", "canceled":
		a.freeSlot(ctx, token, req.key)
		_ = a.rdb.Del(ctx, stateKey).Err()
		return nil, failure(job, req.collection)
	}
	return nil, core.Pending(pollEvery, fmt.Sprintf("Harmony job %s is %s, %d%%", jobID, job.Status, job.Progress))
}

// gesdiscApproval is where an Earthdata account accepts the GES DISC archive's
// terms. Until it does, GES DISC answers every file request with 403 "EULA
// Acceptance Failure", and Harmony's subsetter fails on every granule.
const gesdiscApproval = "https://urs.earthdata.nasa.gov/approve_app?client_id=e2WVk8Pw6weeLUKZYOxvTQ"

// failure explains a failed Harmony job. When the granules failed with 403,
// the data archive refused the account, which the user can fix themselves.
func failure(j *job, collection string) error {
	detail := j.Message
	if len(j.Errors) > 0 {
		detail += ": " + j.Errors[0].Message
	}
	for _, e := range j.Errors {
		if !strings.Contains(e.Message, "403") {
			continue
		}
		msg := "NASA's data archive refused this Earthdata account. The account has to accept the archive's terms in Earthdata Login once."
		if strings.HasSuffix(collection, "-GES_DISC") {
			msg = "NASA's GES DISC archive refused this Earthdata account. Approve the \"NASA GESDISC DATA ARCHIVE\" app once at " + gesdiscApproval + ", then try again."
		}
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false, msg,
			"Harmony job %s %s, granules failed with 403: %s", j.JobID, j.Status, e.Message)
	}
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
		"NASA's Harmony service couldn't process this request.", "Harmony job %s %s: %s", j.JobID, j.Status, detail)
}

// start counts granules, takes a slot and submits the job. Area averages
// skip the count: Giovanni reads the granules itself and returns one file.
func (a *Adapter) start(ctx context.Context, req *request, token, stateKey string) error {
	hits := 0
	if !a.mode.areaAverage {
		var err error
		if hits, err = a.granules(ctx, token, req); err != nil {
			return err
		}
		if hits == 0 {
			return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
				"There's no data for this place and time range.", "CMR found no granules")
		}
	}
	if req.maxGranules > 0 && hits > req.maxGranules {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_TOO_LARGE, false,
			fmt.Sprintf("That would fetch %d files, more than the limit of %d. Pick a shorter time range.", hits, req.maxGranules),
			"%d granules over MAX_GRANULES %d", hits, req.maxGranules)
	}
	ok, err := a.takeSlot(ctx, token, req.key)
	if err != nil {
		return core.Transient(err)
	}
	if !ok {
		return core.Pending(slotWait, "every Harmony slot for this token is busy")
	}
	jobID, err := a.submit(ctx, token, req)
	if err != nil {
		a.freeSlot(ctx, token, req.key)
		return err
	}
	if err := a.rdb.HSet(ctx, stateKey, "job_id", jobID, "granules", hits).Err(); err != nil {
		return core.Transient(err)
	}
	_ = a.rdb.Expire(ctx, stateKey, stateTTL).Err()
	return core.Pending(submitCheck, fmt.Sprintf("submitted Harmony job %s (%s, %d granules counted)", jobID, a.mode.version, hits))
}

// token returns the user's own token when the task has one, else the team's.
func (a *Adapter) token(ctx context.Context, task *ingestv1.IngestTask) (string, error) {
	if key := task.GetEarthdataTokenKey(); key != "" {
		t, err := a.rdb.Get(ctx, key).Result()
		if err == nil && t != "" {
			return t, nil
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return "", core.Transient(err)
		}
		// The user's token expired from Redis. The team token still works.
	}
	if a.cfg.TeamToken == "" {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
			"Fetching NASA data needs an Earthdata token.", "EARTHDATA_TOKEN is empty and the task has no user token")
	}
	return a.cfg.TeamToken, nil
}

// request is a validated task.
type request struct {
	key         string
	collection  string
	variables   []string
	bbox        *commonv1.BBox
	start, end  string
	maxGranules int
}

func newRequest(task *ingestv1.IngestTask, m mode) (*request, error) {
	r := &request{bbox: task.GetBbox()}
	if m.areaAverage {
		g := task.GetGiovanniArea()
		r.collection = g.GetCollectionId()
		if g.GetDataFieldId() != "" {
			r.variables = []string{g.GetDataFieldId()}
		}
	} else {
		h := task.GetHarmonySubset()
		r.collection, r.variables, r.maxGranules = h.GetCollectionId(), h.GetVariables(), int(h.GetMaxGranules())
	}
	var ok1, ok2 bool
	r.start, ok1 = isoDate(task.GetDates().GetStart(), false)
	r.end, ok2 = isoDate(task.GetDates().GetEnd(), true)
	if r.collection == "" || len(r.variables) == 0 || r.bbox == nil || !ok1 || !ok2 {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"The job couldn't start.", "%s task needs a collection, variables, a rectangle and dates", m.version)
	}
	var err error
	if r.key, err = core.CacheKey(task, m.version); err != nil {
		return nil, err
	}
	return r, nil
}

// isoDate formats a date as the first or last second of that day, in UTC.
func isoDate(d *commonv1.Date, endOfDay bool) (string, bool) {
	if d == nil || d.GetYear() == 0 {
		return "", false
	}
	clock := "00:00:00"
	if endOfDay {
		clock = "23:59:59"
	}
	return fmt.Sprintf("%04d-%02d-%02dT%sZ", d.GetYear(), d.GetMonth(), d.GetDay(), clock), true
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// granules asks CMR how many granules the request covers.
func (a *Adapter) granules(ctx context.Context, token string, r *request) (int, error) {
	q := url.Values{
		"collection_concept_id": {r.collection},
		"bounding_box":          {strings.Join([]string{num(r.bbox.GetWest()), num(r.bbox.GetSouth()), num(r.bbox.GetEast()), num(r.bbox.GetNorth())}, ",")},
		"temporal":              {r.start + "," + r.end},
		"page_size":             {"0"},
	}
	resp, err := a.get(ctx, token, a.cfg.CMRURL+"/search/granules.json?"+q.Encode())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := statusError(resp, "CMR"); err != nil {
		return 0, err
	}
	hits, err := strconv.Atoi(resp.Header.Get("CMR-Hits"))
	if err != nil {
		return 0, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA's search service returned something unexpected.", "CMR-Hits header %q", resp.Header.Get("CMR-Hits"))
	}
	return hits, nil
}

// submit starts an async HOSS job and returns its ID.
func (a *Adapter) submit(ctx context.Context, token string, r *request) (string, error) {
	q := url.Values{
		"variable":    {strings.Join(r.variables, ",")},
		"subset":      {"lat(" + num(r.bbox.GetSouth()) + ":" + num(r.bbox.GetNorth()) + ")", "lon(" + num(r.bbox.GetWest()) + ":" + num(r.bbox.GetEast()) + ")", `time("` + r.start + `":"` + r.end + `")`},
		"forceAsync":  {"true"},
		"skipPreview": {"true"},
	}
	if a.mode.areaAverage {
		q.Set("average", "area")
	}
	u := fmt.Sprintf("%s/%s/ogc-api-coverages/1.0.0/collections/parameter_vars/coverage/rangeset?%s",
		a.cfg.HarmonyURL, url.PathEscape(r.collection), q.Encode())
	resp, err := a.get(ctx, token, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if strings.Contains(strings.ToLower(string(body)), "no matching granules") {
			return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
				"There's no data for this place and time range.", "Harmony: %s", body)
		}
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INVALID_REQUEST, false,
			"NASA's Harmony service refused this request.", "Harmony 400: %s", body)
	}
	if err := statusError(resp, "Harmony"); err != nil {
		return "", err
	}
	var j job
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil || j.JobID == "" {
		return "", core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA's Harmony service returned something unexpected.", "decode Harmony job: %v", err)
	}
	return j.JobID, nil
}

type job struct {
	JobID    string `json:"jobID"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Progress int    `json:"progress"`
	Links    []link `json:"links"`
	// Per-granule failures, listed on failed jobs.
	Errors []struct {
		URL     string `json:"url"`
		Message string `json:"message"`
	} `json:"errors"`
}

type link struct {
	Href     string `json:"href"`
	Rel      string `json:"rel"`
	Type     string `json:"type"`
	Temporal *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"temporal"`
}

var errJobGone = errors.New("harmony job not found")

// job reads a job's status. When it's done, it follows the "next" links so
// Links holds every output file.
func (a *Adapter) job(ctx context.Context, token, id string) (*job, error) {
	next := fmt.Sprintf("%s/jobs/%s?page=1&limit=2000", a.cfg.HarmonyURL, url.PathEscape(id))
	var out *job
	for next != "" {
		resp, err := a.get(ctx, token, next)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, errJobGone
		}
		if err := statusError(resp, "Harmony"); err != nil {
			resp.Body.Close()
			return nil, err
		}
		var page job
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
				"NASA's Harmony service returned something unexpected.", "decode Harmony job %s: %v", id, err)
		}
		if out == nil {
			out = &page
		} else {
			out.Links = append(out.Links, page.Links...)
		}
		next = ""
		if page.Status == "successful" || page.Status == "complete_with_errors" {
			for _, l := range page.Links {
				if l.Rel == "next" {
					next = l.Href
				}
			}
		}
	}
	return out, nil
}

// download streams every data link into sink, a few at a time.
func (a *Adapter) download(ctx context.Context, token string, j *job, sink core.Sink) error {
	var links []link
	for _, l := range j.Links {
		if l.Rel == "data" && strings.HasPrefix(l.Href, "http") {
			links = append(links, l)
		}
	}
	if len(links) == 0 {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_NO_DATA, false,
			"There's no data for this place and time range.", "Harmony job %s has no output files", j.JobID)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, max(a.cfg.Downloads, 1))
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, l := range links {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := a.downloadOne(ctx, token, l, sink); err != nil {
				once.Do(func() { firstErr = err; cancel() })
			}
		})
	}
	wg.Wait()
	return firstErr
}

func (a *Adapter) downloadOne(ctx context.Context, token string, l link, sink core.Sink) error {
	u, err := url.Parse(l.Href)
	if err != nil {
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			"NASA's Harmony service returned something unexpected.", "bad output link %q", l.Href)
	}
	resp, err := a.get(ctx, token, l.Href)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusError(resp, "Harmony download"); err != nil {
		return err
	}
	// Giovanni labels its CSV "GEOTIFF/CSV", which isn't a MIME type.
	contentType := l.Type
	switch {
	case a.mode.areaAverage:
		contentType = "text/csv"
	case contentType == "":
		contentType = "application/x-netcdf4"
	}
	return sink.Put(ctx, core.File{
		Name:        path.Base(u.Path),
		Body:        resp.Body,
		Size:        resp.ContentLength,
		ContentType: contentType,
		Coverage:    coverage(l),
	})
}

// coverage turns a link's temporal extent into the dates its file covers.
func coverage(l link) *commonv1.DateRange {
	if l.Temporal == nil {
		return nil
	}
	start, err1 := time.Parse(time.RFC3339, l.Temporal.Start)
	end, err2 := time.Parse(time.RFC3339, l.Temporal.End)
	if err1 != nil || err2 != nil {
		return nil
	}
	return &commonv1.DateRange{Start: date(start), End: date(end)}
}

func date(t time.Time) *commonv1.Date {
	t = t.UTC()
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}

func (a *Adapter) get(ctx context.Context, token, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_INTERNAL, false,
			"Something went wrong while fetching the data.", "build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.http.Do(req)
	if errors.Is(err, errLogin) {
		return nil, unauthenticated(err)
	}
	if err != nil {
		return nil, core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"Couldn't reach NASA's servers.", "GET %s: %w", redact(u), err)
	}
	return resp, nil
}

// statusError maps a response status to a core error, or nil for 2xx.
func statusError(resp *http.Response, service string) error {
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c == http.StatusUnauthorized || c == http.StatusForbidden:
		return unauthenticated(fmt.Errorf("%s returned %d", service, c))
	case c == http.StatusTooManyRequests:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, true,
			"NASA's servers are rate-limiting us. Try again in a few minutes.", "%s returned 429", service)
	case c >= 500:
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, true,
			"NASA's servers had a problem.", "%s returned %d", service, c)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UPSTREAM, false,
			"NASA's servers refused the request.", "%s returned %d: %s", service, c, body)
	}
}

func unauthenticated(err error) error {
	return core.Errorf(gatewayv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED, false,
		"NASA rejected the Earthdata token. Check that it's current.", "%w", err)
}

// redact drops the query string, which can be long and isn't needed in logs.
func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// Slots are a sorted set per token, so at most Concurrency Harmony jobs run
// at once on one person's quota. Members are request keys, scored by when
// they took the slot.
var takeSlotScript = redis.NewScript(`
local slots, member, now, stale, limit = KEYS[1], ARGV[1], tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4])
redis.call('ZREMRANGEBYSCORE', slots, '-inf', now - stale)
if redis.call('ZSCORE', slots, member) then return 1 end
if redis.call('ZCARD', slots) >= limit then return 0 end
redis.call('ZADD', slots, now, member)
redis.call('EXPIRE', slots, stale)
return 1
`)

func (a *Adapter) takeSlot(ctx context.Context, token, member string) (bool, error) {
	ok, err := takeSlotScript.Run(ctx, a.rdb, []string{slotsKey(token)},
		member, time.Now().Unix(), int64(slotStale.Seconds()), max(a.cfg.Concurrency, 1)).Int()
	return ok == 1, err
}

func (a *Adapter) freeSlot(ctx context.Context, token, member string) {
	_ = a.rdb.ZRem(ctx, slotsKey(token), member).Err()
}

// slotsKey names a token's slots by a hash, so the token never appears in
// Redis keys.
func slotsKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "td:ingest:harmony:slots:" + hex.EncodeToString(sum[:8])
}
