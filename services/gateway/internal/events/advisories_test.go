package events

import (
	"testing"
	"time"

	gatewayv1 "github.com/TheBaronessHotel/trend-detective/gen/go/trenddetective/gateway/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestExhaustedBuildsTheMissingReport(t *testing.T) {
	task := &gatewayv1.Task{Meta: &gatewayv1.TaskMeta{JobId: "j", TaskId: "j-preprocess-0", Stage: gatewayv1.Stage_STAGE_PREPROCESS}}
	data, err := protojson.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	adv := maxDeliveries{Stream: "TASKS", Consumer: "preprocess", StreamSeq: 42, Deliveries: 3}

	d, err := exhausted(adv, data, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.GetMeta().GetTaskId() != "j-preprocess-0" || d.GetAttempt() != 3 {
		t.Errorf("report: %v", d)
	}
	e := d.GetError()
	if e.GetCode() != gatewayv1.ErrorCode_ERROR_CODE_RETRIES_EXHAUSTED || !e.GetRetryable() || e.GetUserMessage() == "" {
		t.Errorf("error: %v", e)
	}
}

func TestExhaustedRejectsGarbage(t *testing.T) {
	adv := maxDeliveries{StreamSeq: 1}
	if _, err := exhausted(adv, []byte("not json"), time.Now()); err == nil {
		t.Error("accepted a message that isn't a Task")
	}
	if _, err := exhausted(adv, []byte(`{"meta": {}}`), time.Now()); err == nil {
		t.Error("accepted a Task without an ID")
	}
}
