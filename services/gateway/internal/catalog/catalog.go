// Package catalog loads catalog.yaml and checks it. A broken catalog stops
// the gateway at startup, instead of failing jobs later.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	commonv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/common/v1"
	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	preprocessv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/preprocess/v1"
	"github.com/TheBaronessHotel/trend-detective/internal/sources"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

// Entry is one variable with everything the gateway needs to start a job.
type Entry struct {
	// Variable is what ListVariables returns.
	Variable *gatewayv1.Variable
	// SourceUnits are the units of the raw values.
	SourceUnits string
	// LLMHints is extra context for the narrator.
	LLMHints string

	adapters    map[ingestv1.Source]*ingestv1.IngestTask
	filters     *preprocessv1.FilterChain
	aggregation *preprocessv1.Aggregation
	tests       *analysisv1.TestPlan
}

// IngestTemplate returns a new IngestTask with only the adapter settings for
// source filled in. It reports false when the variable doesn't offer source.
func (e *Entry) IngestTemplate(source ingestv1.Source) (*ingestv1.IngestTask, bool) {
	t, ok := e.adapters[source]
	if !ok {
		return nil, false
	}
	return proto.CloneOf(t), true
}

// Filters returns a copy of the default filter chain.
func (e *Entry) Filters() *preprocessv1.FilterChain { return proto.CloneOf(e.filters) }

// Aggregation returns a copy of the variable's aggregation settings, or nil
// when it uses the defaults.
func (e *Entry) Aggregation() *preprocessv1.Aggregation {
	if e.aggregation == nil {
		return nil
	}
	return proto.CloneOf(e.aggregation)
}

// Tests returns a copy of the default test plan.
func (e *Entry) Tests() *analysisv1.TestPlan { return proto.CloneOf(e.tests) }

// Catalog is the loaded catalog.
type Catalog struct {
	byID  map[string]*Entry
	order []string
}

// Get returns the entry for a variable ID.
func (c *Catalog) Get(id string) (*Entry, bool) {
	e, ok := c.byID[id]
	return e, ok
}

// Variables returns every variable, in file order.
func (c *Catalog) Variables() []*gatewayv1.Variable {
	out := make([]*gatewayv1.Variable, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, proto.CloneOf(c.byID[id].Variable))
	}
	return out
}

type file struct {
	Variables []rawEntry `json:"variables"`
}

type rawEntry struct {
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Definition    string                      `json:"definition"`
	Units         string                      `json:"units"`
	Step          string                      `json:"step"`
	Available     struct{ Start, End string } `json:"available"`
	Grid          bool                        `json:"grid"`
	Domain        string                      `json:"domain"`
	DefaultSource string                      `json:"default_source"`
	Sources       map[string]json.RawMessage  `json:"sources"`
	SourceUnits   string                      `json:"source_units"`
	Filters       json.RawMessage             `json:"filters"`
	Aggregation   json.RawMessage             `json:"aggregation"`
	Tests         json.RawMessage             `json:"tests"`
	LLMHints      string                      `json:"llm_hints"`
	DatasetURL    string                      `json:"dataset_url"`
	PixelSize     struct{ Lat, Lon float64 }  `json:"pixel_size"`
}

// Parse reads catalog YAML. It rejects unknown fields and reports every
// problem it finds, not just the first.
func Parse(data []byte) (*Catalog, error) {
	var f file
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if len(f.Variables) == 0 {
		return nil, errors.New("catalog: no variables")
	}
	c := &Catalog{byID: map[string]*Entry{}}
	var errs []error
	for i, r := range f.Variables {
		e, err := r.entry()
		if err != nil {
			errs = append(errs, fmt.Errorf("variable %d (%q): %w", i+1, r.ID, err))
			continue
		}
		if _, dup := c.byID[r.ID]; dup {
			errs = append(errs, fmt.Errorf("variable %d: duplicate id %q", i+1, r.ID))
			continue
		}
		c.byID[r.ID] = e
		c.order = append(c.order, r.ID)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("catalog: %w", errors.Join(errs...))
	}
	return c, nil
}

func (r rawEntry) entry() (*Entry, error) {
	var errs []error
	for name, v := range map[string]string{"id": r.ID, "name": r.Name, "definition": r.Definition, "units": r.Units} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	step, err := parseStep(r.Step)
	errs = appendErr(errs, err)
	domain, err := parseDomain(r.Domain)
	errs = appendErr(errs, err)
	available, err := parseRange(r.Available.Start, r.Available.End)
	errs = appendErr(errs, err)

	adapters := map[ingestv1.Source]*ingestv1.IngestTask{}
	for name, raw := range r.Sources {
		src, ok := sources.Parse(name)
		if !ok {
			errs = append(errs, fmt.Errorf("sources: unknown adapter %q", name))
			continue
		}
		t, err := ingestTemplate(src, raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("sources.%s: %w", name, err))
			continue
		}
		adapters[src] = t
	}
	defaultSource, ok := sources.Parse(r.DefaultSource)
	if _, offered := adapters[defaultSource]; !ok || !offered {
		errs = append(errs, fmt.Errorf("default_source %q must be one of the keys under sources", r.DefaultSource))
	}

	filters := &preprocessv1.FilterChain{}
	errs = appendErr(errs, unmarshalOptional("filters", r.Filters, filters))
	var aggregation *preprocessv1.Aggregation
	if len(r.Aggregation) > 0 {
		aggregation = &preprocessv1.Aggregation{}
		errs = appendErr(errs, unmarshalOptional("aggregation", r.Aggregation, aggregation))
	}
	tests := &analysisv1.TestPlan{}
	errs = appendErr(errs, unmarshalOptional("tests", r.Tests, tests))
	if r.Grid && (r.PixelSize.Lat <= 0 || r.PixelSize.Lon <= 0) {
		errs = append(errs, errors.New("a grid variable needs pixel_size, its native pixel size in degrees"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &Entry{
		Variable: &gatewayv1.Variable{
			Id:            r.ID,
			Name:          r.Name,
			Definition:    strings.TrimSpace(r.Definition),
			Units:         r.Units,
			Step:          step,
			Available:     available,
			Grid:          r.Grid,
			Domain:        domain,
			DefaultSource: defaultSource,
			Sources:       slices.Sorted(maps.Keys(adapters)),
			DatasetUrl:    r.DatasetURL,
			PixelLatDeg:   r.PixelSize.Lat,
			PixelLonDeg:   r.PixelSize.Lon,
			// GetBlockPixels is 0 for a nil aggregation.
			DefaultBlockPixels: aggregation.GetBlockPixels(),
		},
		SourceUnits: r.SourceUnits,
		LLMHints:    strings.TrimSpace(r.LLMHints),
		adapters:    adapters,
		filters:     filters,
		aggregation: aggregation,
		tests:       tests,
	}, nil
}

// ingestTemplate builds an IngestTask holding only the adapter's settings.
func ingestTemplate(src ingestv1.Source, raw json.RawMessage) (*ingestv1.IngestTask, error) {
	t := &ingestv1.IngestTask{}
	var settings proto.Message
	switch src {
	case ingestv1.Source_SOURCE_HARMONY_SUBSET:
		m := &ingestv1.HarmonySubset{}
		t.Adapter, settings = &ingestv1.IngestTask_HarmonySubset{HarmonySubset: m}, m
	case ingestv1.Source_SOURCE_GIOVANNI_AREA:
		m := &ingestv1.GiovanniArea{}
		t.Adapter, settings = &ingestv1.IngestTask_GiovanniArea{GiovanniArea: m}, m
	case ingestv1.Source_SOURCE_GIOVANNI_POINT:
		m := &ingestv1.GiovanniPoint{}
		t.Adapter, settings = &ingestv1.IngestTask_GiovanniPoint{GiovanniPoint: m}, m
	case ingestv1.Source_SOURCE_POWER:
		m := &ingestv1.Power{}
		t.Adapter, settings = &ingestv1.IngestTask_Power{Power: m}, m
	case ingestv1.Source_SOURCE_CSV_UPLOAD:
		m := &ingestv1.CsvUpload{}
		t.Adapter, settings = &ingestv1.IngestTask_CsvUpload{CsvUpload: m}, m
	case ingestv1.Source_SOURCE_BUNDLE:
		m := &ingestv1.Bundle{}
		t.Adapter, settings = &ingestv1.IngestTask_Bundle{Bundle: m}, m
	case ingestv1.Source_SOURCE_APPEEARS:
		m := &ingestv1.Appeears{}
		t.Adapter, settings = &ingestv1.IngestTask_Appeears{Appeears: m}, m
	default:
		return nil, fmt.Errorf("no settings message for %v", src)
	}
	if err := unmarshalOptional("settings", raw, settings); err != nil {
		return nil, err
	}
	return t, nil
}

func unmarshalOptional(what string, raw json.RawMessage, msg proto.Message) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := protojson.Unmarshal(raw, msg); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func parseStep(s string) (commonv1.TimeStep, error) {
	switch s {
	case "daily":
		return commonv1.TimeStep_TIME_STEP_DAILY, nil
	case "weekly":
		return commonv1.TimeStep_TIME_STEP_WEEKLY, nil
	case "monthly":
		return commonv1.TimeStep_TIME_STEP_MONTHLY, nil
	}
	return 0, fmt.Errorf("step must be daily, weekly or monthly, got %q", s)
}

func parseDomain(s string) (gatewayv1.Domain, error) {
	switch s {
	case "land":
		return gatewayv1.Domain_DOMAIN_LAND, nil
	case "ocean":
		return gatewayv1.Domain_DOMAIN_OCEAN, nil
	case "global":
		return gatewayv1.Domain_DOMAIN_GLOBAL, nil
	}
	return 0, fmt.Errorf("domain must be land, ocean or global, got %q", s)
}

func parseRange(start, end string) (*commonv1.DateRange, error) {
	s, err1 := time.Parse(time.DateOnly, start)
	e, err2 := time.Parse(time.DateOnly, end)
	if err1 != nil || err2 != nil || e.Before(s) {
		return nil, fmt.Errorf("available needs start and end as YYYY-MM-DD, start first, got %q to %q", start, end)
	}
	return &commonv1.DateRange{Start: toDate(s), End: toDate(e)}, nil
}

func toDate(t time.Time) *commonv1.Date {
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}

func appendErr(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}
