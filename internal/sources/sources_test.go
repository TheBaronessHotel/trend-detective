package sources

import (
	"testing"

	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
)

func TestNameAndParseRoundTrip(t *testing.T) {
	if got := Name(ingestv1.Source_SOURCE_HARMONY_SUBSET); got != "harmony-subset" {
		t.Errorf("Name: got %q", got)
	}
	for v := range ingestv1.Source_name {
		s := ingestv1.Source(v)
		if s == ingestv1.Source_SOURCE_UNSPECIFIED {
			continue
		}
		if got, ok := Parse(Name(s)); !ok || got != s {
			t.Errorf("Parse(Name(%v)) = %v, %v", s, got, ok)
		}
	}
	if _, ok := Parse("unspecified"); ok {
		t.Error("Parse accepted the unspecified source")
	}
}
