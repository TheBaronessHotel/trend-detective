package catalog

import (
	"strings"
	"testing"

	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
	"github.com/TheBaronessHotel/trend-detective/services/gateway"
)

func TestShippedCatalogLoads(t *testing.T) {
	c, err := Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Variables()) == 0 {
		t.Fatal("no variables")
	}
	for _, v := range c.Variables() {
		e, _ := c.Get(v.GetId())
		if _, ok := e.IngestTemplate(v.GetDefaultSource()); !ok {
			t.Errorf("%s: no settings for its default source", v.GetId())
		}
		if len(e.Tests().GetSeries()) == 0 {
			t.Errorf("%s: no default series tests", v.GetId())
		}
	}
}

func TestTemplateCarriesAdapterSettings(t *testing.T) {
	c, err := Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.Get("air-temperature-power")
	if !ok {
		t.Fatal("air-temperature-power is missing")
	}
	tmpl, ok := e.IngestTemplate(ingestv1.Source_SOURCE_BUNDLE)
	if !ok {
		t.Fatal("no bundle template")
	}
	b := tmpl.GetBundle()
	if b.GetName() != "power-delhi-monthly" || len(b.GetVariables()) != 1 || b.GetVariables()[0] != "T2M" {
		t.Errorf("bundle settings: %v", b)
	}
	// The template is a copy, so changing it leaves the catalog alone.
	b.Name = "changed"
	again, _ := e.IngestTemplate(ingestv1.Source_SOURCE_BUNDLE)
	if again.GetBundle().GetName() != "power-delhi-monthly" {
		t.Error("IngestTemplate returned the catalog's own message")
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	valid := `
variables:
  - id: x
    name: X
    topic: Air
    measurement: Temperature
    product: P
    definition: d
    units: u
    step: monthly
    available: { start: 2000-01-01, end: 2001-01-01 }
    domain: land
    default_source: bundle
    sources:
      bundle: { name: b }
`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	tests := map[string]struct{ old, new, want string }{
		"unknown field":           {"    units: u\n", "    units: u\n    colour: red\n", "colour"},
		"default not offered":     {"default_source: bundle", "default_source: power", "default_source"},
		"unknown adapter":         {"      bundle: { name: b }", "      bundle: { name: b }\n      ftp: {}", "ftp"},
		"bad step":                {"step: monthly", "step: hourly", "step"},
		"end before start":        {"end: 2001-01-01", "end: 1999-01-01", "available"},
		"bad adapter settings":    {"{ name: b }", "{ nmae: b }", "nmae"},
		"missing definition":      {"    definition: d\n", "", "definition"},
		"bad filter op":           {"    units: u\n", "    units: u\n    filters: { ops: [ { blur: {} } ] }\n", "filters"},
		"grid without pixel size": {"    domain: land\n", "    domain: land\n    grid: true\n", "pixel_size"},
		"unknown topic":           {"topic: Air", "topic: Weather", "topic"},
		"measurement of another":  {"measurement: Temperature", "measurement: Gases", "measurement"},
		"missing product":         {"    product: P\n", "", "product"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(valid, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("want an error mentioning %q, got %v", tt.want, err)
			}
		})
	}
}

// The app previews the grid from these before a job runs.
func TestGridVariablesCarryPixelSizeAndBlock(t *testing.T) {
	c, err := Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.Variables() {
		if v.GetGrid() && (v.GetPixelLatDeg() <= 0 || v.GetPixelLonDeg() <= 0) {
			t.Errorf("%s: grid without a pixel size", v.GetId())
		}
	}
	gpm, ok := c.Get("precipitation-gpm")
	if !ok {
		t.Fatal("no precipitation-gpm")
	}
	if v := gpm.Variable; v.GetPixelLatDeg() != 0.1 || v.GetDefaultBlockPixels() != 4 {
		t.Errorf("precipitation-gpm: %v", v)
	}
}

// The browser groups variables by these, so every shipped entry has them.
func TestShippedVariablesHaveATopic(t *testing.T) {
	c, err := Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.Variables() {
		if v.GetTopic() == "" || v.GetMeasurement() == "" || v.GetProduct() == "" {
			t.Errorf("%s: topic %q, measurement %q, product %q", v.GetId(), v.GetTopic(), v.GetMeasurement(), v.GetProduct())
		}
	}
}

func TestTopicsInUseSkipEmptyOnes(t *testing.T) {
	c, err := Parse(gateway.CatalogYAML)
	if err != nil {
		t.Fatal(err)
	}
	topics := c.TopicsInUse()
	if len(topics) == 0 || topics[0].GetName() != "Air" || topics[0].GetMeasurements()[0] != "Temperature" {
		t.Fatalf("topics: %v", topics)
	}
	for _, tp := range topics {
		if len(tp.GetMeasurements()) == 0 {
			t.Errorf("topic %q has no measurements", tp.GetName())
		}
	}
}
