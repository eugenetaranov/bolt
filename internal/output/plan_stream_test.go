package output

import (
	"bytes"
	"strings"
	"testing"
)

func planTestTasks() []PlannedTask {
	return []PlannedTask{
		{Name: "install nginx", Module: "apt", Status: "will_change"},
		{Name: "scan ports", Module: "command", Status: "will_run"},
		{Name: "debian only", Module: "apt", Status: "will_skip", Reason: "skipped (tag)"},
		{Name: "other role", Module: "apt", Status: "will_skip", Reason: "skipped (role)"},
	}
}

// TestPlan_OmitsTagSkips verifies tag-filtered tasks are omitted from the plan
// body and reported as "N filtered" in the summary, while role-filtered tasks
// remain visible.
func TestPlan_OmitsTagSkips(t *testing.T) {
	var buf bytes.Buffer
	o := New(&buf)
	o.SetColor(false)
	o.DisplayPlan(planTestTasks(), false)

	got := buf.String()
	if strings.Contains(got, "debian only") {
		t.Errorf("tag-filtered task should be omitted from the plan:\n%s", got)
	}
	if !strings.Contains(got, "install nginx") || !strings.Contains(got, "scan ports") {
		t.Errorf("active tasks should be shown:\n%s", got)
	}
	if !strings.Contains(got, "other role") {
		t.Errorf("role-filtered task should still be shown:\n%s", got)
	}
	if !strings.Contains(got, "1 filtered") {
		t.Errorf("summary should report filtered count:\n%s", got)
	}
	// The tag-skip must not be counted toward "to skip"; the role-skip is.
	if !strings.Contains(got, "1 to skip") {
		t.Errorf("role-skip should count as 'to skip':\n%s", got)
	}
}

// TestPlan_StreamingEqualsBatch verifies the streaming API
// (PlanStart → PlanLine* → PlanEnd) produces byte-identical output to the batch
// DisplayPlan, so the executor can stream without changing what's rendered.
func TestPlan_StreamingEqualsBatch(t *testing.T) {
	tasks := planTestTasks()

	var batch bytes.Buffer
	ob := New(&batch)
	ob.SetColor(false)
	ob.DisplayPlan(tasks, false)

	var stream bytes.Buffer
	os := New(&stream)
	os.SetColor(false)
	os.PlanStart(false)
	for _, tk := range tasks {
		os.PlanLine(tk)
	}
	os.PlanEnd(tasks, false)

	if batch.String() != stream.String() {
		t.Errorf("streaming output differs from batch:\nbatch:\n%q\nstream:\n%q", batch.String(), stream.String())
	}
}
