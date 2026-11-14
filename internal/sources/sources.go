// Package sources converts between ingest.v1.Source values and the adapter
// names that the catalog and the raw bucket layout use, like "harmony-subset".
package sources

import (
	"strings"

	ingestv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/ingest/v1"
)

// Name turns SOURCE_HARMONY_SUBSET into "harmony-subset".
func Name(s ingestv1.Source) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s.String(), "SOURCE_")), "_", "-")
}

// Parse turns "harmony-subset" into SOURCE_HARMONY_SUBSET. It reports false
// for a name that isn't a source.
func Parse(name string) (ingestv1.Source, bool) {
	for v := range ingestv1.Source_name {
		s := ingestv1.Source(v)
		if s != ingestv1.Source_SOURCE_UNSPECIFIED && Name(s) == name {
			return s, true
		}
	}
	return ingestv1.Source_SOURCE_UNSPECIFIED, false
}
