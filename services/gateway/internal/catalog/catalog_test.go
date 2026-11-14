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
		"unknown field":        {"    units: u\n", "    units: u\n    colour: red\n", "colour"},
		"default not offered":  {"default_source: bundle", "default_source: power", "default_source"},
		"unknown adapter":      {"      bundle: { name: b }", "      bundle: { name: b }\n      ftp: {}", "ftp"},
		"bad step":             {"step: monthly", "step: hourly", "step"},
		"end before start":     {"end: 2001-01-01", "end: 1999-01-01", "available"},
		"bad adapter settings": {"{ name: b }", "{ nmae: b }", "nmae"},
		"missing definition":   {"    definition: d\n", "", "definition"},
		"bad filter op":        {"    units: u\n", "    units: u\n    filters: { ops: [ { blur: {} } ] }\n", "filters"},
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
