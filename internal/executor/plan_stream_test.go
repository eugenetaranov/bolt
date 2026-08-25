package executor

import (
	"context"
	"testing"

	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
)

// TestPlanStreaming_CallsOnPlanLine verifies that planTasks streams each planned
// task through pctx.OnPlanLine as it is computed. A tag-filtered task is used so
// no connector/check is required.
func TestPlanStreaming_CallsOnPlanLine(t *testing.T) {
	exec := New()
	exec.Tags = []string{"scan"} // only 'scan' tasks run; others are tag-filtered

	var streamed []output.PlannedTask
	pctx := &PlayContext{
		Host:       "h1",
		Vars:       map[string]any{},
		Registered: map[string]any{},
		Play:       &playbook.Play{},
		OnPlanLine: func(pt output.PlannedTask) { streamed = append(streamed, pt) },
	}

	tasks := []*playbook.Task{
		{Name: "build step", Module: "command", Params: map[string]any{"cmd": "true"}, Tags: []string{"build"}},
	}

	plan := exec.planTasks(context.Background(), pctx, tasks, &nullEmitter{})

	if len(plan) != 1 {
		t.Fatalf("expected 1 planned task, got %d", len(plan))
	}
	if len(streamed) != 1 {
		t.Fatalf("expected OnPlanLine to be called once, got %d", len(streamed))
	}
	if streamed[0].Status != "will_skip" || streamed[0].Reason != "skipped (tag)" {
		t.Errorf("streamed task = %+v, want tag-skip", streamed[0])
	}
	// The streamed task must be the same one collected into the plan.
	if streamed[0].Name != plan[0].Name {
		t.Errorf("streamed %q != planned %q", streamed[0].Name, plan[0].Name)
	}
}

// TestPlanStreaming_Disabled verifies planTasks does not require OnPlanLine
// (nil), preserving the batch path used by multi-host/JSON.
func TestPlanStreaming_Disabled(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Host:       "h1",
		Vars:       map[string]any{},
		Registered: map[string]any{},
		Play:       &playbook.Play{},
		// OnPlanLine nil
	}
	tasks := []*playbook.Task{
		{Name: "skip me", Module: "command", Params: map[string]any{"cmd": "true"}, Tags: []string{"build"}},
	}
	exec.Tags = []string{"other"}
	plan := exec.planTasks(context.Background(), pctx, tasks, &nullEmitter{})
	if len(plan) != 1 {
		t.Fatalf("expected 1 planned task, got %d", len(plan))
	}
}
