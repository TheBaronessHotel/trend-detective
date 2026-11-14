// Package prompt turns analysis results into an LLM request. The model gets
// structured facts and must never compute numbers itself.
package prompt

import (
	analysisv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/analysis/v1"
	narratorv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/narrator/v1"

	"github.com/TheBaronessHotel/trend-detective/services/narrator/internal/llm"
)

// Input is everything the narrator knows about one analysis.
// Series is loaded from AnalysisResult.series; Grid is optional (may be nil).
type Input struct {
	Req    *narratorv1.NarrateRequest
	Series *analysisv1.SeriesAnalysis
	Grid   *analysisv1.GridAnalysis
}

// Builder is implemented in this package (NAR-2).
type Builder interface {
	// Build returns the request (system + history + question; empty question
	// means "overview") and the list of allowed facts for verification.
	Build(in Input) (llm.Request, []Fact, error)
}

// Fact is one number the answer may legitimately mention.
type Fact struct {
	Name  string // e.g. "regression.slope_per_decade"
	Value float64
	Units string
}
