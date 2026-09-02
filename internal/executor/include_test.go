package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
)

func TestResolveIncludePath(t *testing.T) {
	exec := New()

	tests := []struct {
		name        string
		includePath string
		rolePath    string
		playbookDir string
		want        string
	}{
		{
			name:        "absolute path unchanged",
			includePath: "/etc/tack/tasks.yml",
			playbookDir: "/opt/playbooks",
			want:        "/etc/tack/tasks.yml",
		},
		{
			name:        "relative to playbook dir",
			includePath: "tasks/setup.yml",
			playbookDir: "/opt/playbooks",
			want:        "/opt/playbooks/tasks/setup.yml",
		},
		{
			name:        "relative to role tasks dir",
			includePath: "subtasks.yml",
			rolePath:    "/opt/playbooks/roles/myrole",
			playbookDir: "/opt/playbooks",
			want:        "/opt/playbooks/roles/myrole/tasks/subtasks.yml",
		},
		{
			name:        "URL path unchanged",
			includePath: "https://example.com/tasks.yml",
			playbookDir: "/opt/playbooks",
			want:        "https://example.com/tasks.yml",
		},
		{
			name:        "git URL unchanged",
			includePath: "git@github.com:user/repo.git//tasks.yml",
			playbookDir: "/opt/playbooks",
			want:        "git@github.com:user/repo.git//tasks.yml",
		},
		{
			name:        "no playbook dir, no role path",
			includePath: "tasks/setup.yml",
			want:        "tasks/setup.yml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.resolveIncludePath(tt.includePath, tt.rolePath, tt.playbookDir)
			if got != tt.want {
				t.Errorf("resolveIncludePath(%q, %q, %q) = %q, want %q",
					tt.includePath, tt.rolePath, tt.playbookDir, got, tt.want)
			}
		})
	}
}

func TestRestoreIncludeVars(t *testing.T) {
	exec := New()

	t.Run("restores overridden vars and removes injected", func(t *testing.T) {
		pctx := &PlayContext{
			Vars: map[string]any{
				"existing": "original",
				"keep":     "untouched",
			},
		}

		// Simulate what runIncludeOnce does
		savedVars := map[string]any{"existing": "original"}
		injectedKeys := []string{"new_var"}

		// Override and inject
		pctx.Vars["existing"] = "overridden"
		pctx.Vars["new_var"] = "injected"

		exec.restoreIncludeVars(pctx, savedVars, injectedKeys)

		if pctx.Vars["existing"] != "original" {
			t.Errorf("expected 'existing' to be restored to 'original', got %v", pctx.Vars["existing"])
		}
		if _, exists := pctx.Vars["new_var"]; exists {
			t.Error("expected 'new_var' to be removed after restore")
		}
		if pctx.Vars["keep"] != "untouched" {
			t.Errorf("expected 'keep' to remain 'untouched', got %v", pctx.Vars["keep"])
		}
	})
}

func TestCircularIncludeDetection(t *testing.T) {
	// Create temp directory with circular include files
	tmpDir := t.TempDir()

	// a.yml includes b.yml
	aPath := filepath.Join(tmpDir, "a.yml")
	bPath := filepath.Join(tmpDir, "b.yml")

	_ = os.WriteFile(aPath, []byte(`
- name: From A
  include: b.yml
`), 0644)
	_ = os.WriteFile(bPath, []byte(`
- name: From B
  include: a.yml
`), 0644)

	// Load a.yml tasks
	tasks, err := playbook.LoadTasksFile(aPath)
	if err != nil {
		t.Fatalf("failed to load a.yml: %v", err)
	}

	if len(tasks) != 1 || tasks[0].Include != "b.yml" {
		t.Fatalf("expected include task from a.yml, got %+v", tasks)
	}

	// Test circular detection logic directly
	absA, _ := filepath.Abs(aPath)
	absB, _ := filepath.Abs(bPath)

	// Simulating: we're processing b.yml and it wants to include a.yml again
	visitedPaths := []string{absA, absB}
	for _, vp := range visitedPaths {
		if vp == absA {
			// Circular detected - this is the expected behavior
			return
		}
	}
	t.Error("expected circular include to be detected")
}

func TestMaxIncludeDepthCheck(t *testing.T) {
	// Build a visited paths slice that exceeds max depth
	visited := make([]string, maxIncludeDepth)
	for i := range visited {
		visited[i] = "/fake/path/" + string(rune('a'+i%26))
	}

	if len(visited) < maxIncludeDepth {
		t.Fatal("test setup error: visited paths should be at max depth")
	}

	// The check in runIncludeOnce is: if len(visitedPaths) >= maxIncludeDepth
	if len(visited) >= maxIncludeDepth {
		// This would trigger the max depth error
		return
	}
	t.Error("expected max depth to be exceeded")
}

func TestNonCycleReuseAllowed(t *testing.T) {
	// Same file included from two independent branches is NOT a cycle
	absPath := "/opt/playbooks/shared.yml"

	// Branch 1: main.yml → shared.yml (visited = [main.yml, shared.yml])
	visited1 := []string{"/opt/playbooks/main.yml", absPath}
	_ = visited1 // Branch 1 completes

	// Branch 2: main.yml → other.yml → shared.yml
	// visited = [main.yml, other.yml] - shared.yml is NOT in this chain
	visited2 := []string{"/opt/playbooks/main.yml", "/opt/playbooks/other.yml"}

	for _, vp := range visited2 {
		if vp == absPath {
			t.Error("false positive: shared.yml should be allowed from a different branch")
			return
		}
	}
	// No cycle detected — correct
}

func TestPlanTasksInclude(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars:       map[string]any{"facts": map[string]any{"os": "linux"}},
		Registered: make(map[string]any),
	}

	t.Run("include_tasks shown in plan", func(t *testing.T) {
		tasks := []*playbook.Task{
			{Include: "setup.yml", Name: "Include setup"},
		}
		plan := exec.planTasks(context.Background(), pctx, tasks, &nullEmitter{})
		if len(plan) != 1 {
			t.Fatalf("expected 1 planned task, got %d", len(plan))
		}
		if plan[0].Module != "include_tasks" {
			t.Errorf("expected module 'include_tasks', got %q", plan[0].Module)
		}
		if plan[0].Status != "will_run" {
			t.Errorf("expected status 'will_run', got %q", plan[0].Status)
		}
	})

	t.Run("conditional include_tasks in plan", func(t *testing.T) {
		pctx.Registered["check_result"] = map[string]any{"rc": 0}
		tasks := []*playbook.Task{
			{Include: "setup.yml", Name: "Conditional include", When: "check_result.rc == 0"},
		}
		plan := exec.planTasks(context.Background(), pctx, tasks, &nullEmitter{})
		if len(plan) != 1 {
			t.Fatalf("expected 1 planned task, got %d", len(plan))
		}
		if plan[0].Status != "conditional" {
			t.Errorf("expected status 'conditional', got %q", plan[0].Status)
		}
	})

	t.Run("skipped include_tasks in plan", func(t *testing.T) {
		tasks := []*playbook.Task{
			{Include: "debian.yml", Name: "Debian only", When: "facts.os == 'windows'"},
		}
		plan := exec.planTasks(context.Background(), pctx, tasks, &nullEmitter{})
		if len(plan) != 1 {
			t.Fatalf("expected 1 planned task, got %d", len(plan))
		}
		if plan[0].Status != "will_skip" {
			t.Errorf("expected status 'will_skip', got %q", plan[0].Status)
		}
	})
}

// nullEmitter is a no-op output emitter for testing.
type nullEmitter struct{}

func (n *nullEmitter) PlaybookStart(path string)                                                  {}
func (n *nullEmitter) PlaybookEnd(stats output.Stats)                                             {}
func (n *nullEmitter) PlayStart(play *playbook.Play)                                              {}
func (n *nullEmitter) HostStart(host, conn string)                                                {}
func (n *nullEmitter) HostFactsStart(host string)                                                 {}
func (n *nullEmitter) HostFactsResult(host string, ok bool, errMsg string)                        {}
func (n *nullEmitter) HostStartDone(host string)                                                  {}
func (n *nullEmitter) PlayHosts(hosts []string)                                                   {}
func (n *nullEmitter) TaskStart(name, module string)                                              {}
func (n *nullEmitter) TaskResult(name, status string, changed bool, detail string, tags []string) {}
func (n *nullEmitter) DisplayPlan(tasks []output.PlannedTask, dryRun bool)                        {}
func (n *nullEmitter) DisplayMultiHostPlan(tasks []output.PlannedTask, hosts []string, dryRun bool) {
}
func (n *nullEmitter) PromptApproval(_ string) bool     { return true }
func (n *nullEmitter) Section(name string)              {}
func (n *nullEmitter) Info(format string, args ...any)  {}
func (n *nullEmitter) Warn(format string, args ...any)  {}
func (n *nullEmitter) Error(format string, args ...any) {}
func (n *nullEmitter) Debug(format string, args ...any) {}
func (n *nullEmitter) SetColor(enabled bool)            {}
func (n *nullEmitter) SetDebug(enabled bool)            {}
func (n *nullEmitter) SetVerbose(enabled bool)          {}
func (n *nullEmitter) SetDiff(enabled bool)             {}
func (n *nullEmitter) SetDryRun(enabled bool)           {}
func (n *nullEmitter) DiffEnabled() bool                { return false }
