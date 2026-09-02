package executor

import (
	"context"
	"strings"
	"testing"

	_ "github.com/tackhq/tack/internal/module/command"
	"github.com/tackhq/tack/internal/playbook"
)

func TestLoop_AggregatesResults(t *testing.T) {
	exec := New()
	pctx, _ := cwPctx(&cwConn{exitCode: 0, stdout: "x"})
	task := &playbook.Task{
		Module:   "command",
		Params:   map[string]any{"cmd": "echo {{ item }}"},
		Loop:     []any{"a", "b", "c"},
		Register: "out",
	}
	res, err := exec.runTaskLoop(context.Background(), pctx, task, nil)
	if err != nil {
		t.Fatalf("runTaskLoop: %v", err)
	}
	if res.Status != "changed" {
		t.Errorf("expected changed (command always changes), got %s", res.Status)
	}

	out, ok := pctx.Vars["out"].(map[string]any)
	if !ok {
		t.Fatalf("register var not a map: %T", pctx.Vars["out"])
	}
	results, ok := out["results"].([]any)
	if !ok {
		t.Fatalf("results not a list: %T", out["results"])
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 aggregated results, got %d", len(results))
	}
}

func TestLoop_ControlLabel(t *testing.T) {
	exec := New()
	pctx, buf := cwPctx(&cwConn{exitCode: 0, stdout: "x"})
	task := &playbook.Task{
		Module:      "command",
		Params:      map[string]any{"cmd": "true"},
		Loop:        []any{"a", "b"},
		LoopControl: &playbook.LoopControl{Label: "item-{{ item }}"},
	}
	if _, err := exec.runTaskLoop(context.Background(), pctx, task, nil); err != nil {
		t.Fatalf("runTaskLoop: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "item-a") || !strings.Contains(out, "item-b") {
		t.Errorf("expected per-iteration labels in output:\n%s", out)
	}
}
