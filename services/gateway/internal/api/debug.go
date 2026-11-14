package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/TheBaronessHotel/trend-detective/services/gateway/internal/jobs"
	"google.golang.org/protobuf/encoding/protojson"
)

// DebugJobs serves GET /debug/jobs/{id}: the job with every task's stage,
// attempts and errors, plus the raw StageDone events behind it. It's for
// people debugging a job, so the output is indented. Compose publishes the
// gateway on 127.0.0.1 only.
func DebugJobs(orch *jobs.Orchestrator) http.Handler {
	pretty := protojson.MarshalOptions{Multiline: true, Indent: "  "}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job, events, err := orch.Debug(r.Context(), r.PathValue("id"))
		if errors.Is(err, jobs.ErrNotFound) {
			http.Error(w, "no job with that ID", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jobJSON, err := pretty.Marshal(job)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		eventsJSON := make([]json.RawMessage, 0, len(events))
		for _, e := range events {
			b, err := pretty.Marshal(e)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			eventsJSON = append(eventsJSON, b)
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"job": json.RawMessage(jobJSON), "stage_done_events": eventsJSON})
	})
}
