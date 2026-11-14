// Package verify checks that every number in a model answer comes from the facts.
package verify

import "github.com/TheBaronessHotel/trend-detective/services/narrator/internal/prompt"

// Verifier is implemented in this package (NAR-2).
type Verifier interface {
	// Unverified returns number strings found in answer (as written) that
	// match no fact within rounding tolerance (incl. percent, sign, per-year vs
	// per-decade scaling). Years/dates in the requested range and small
	// counting words may be whitelisted.
	Unverified(answer string, facts []prompt.Fact) []string
}
