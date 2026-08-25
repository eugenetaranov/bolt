package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
)

// writeRole creates a minimal role with a single command task under
// rolesDir/<name>/tasks/main.yaml. The task's echo message is the role name so
// output can be asserted on.
func writeRole(t *testing.T, rolesDir, name string) {
	t.Helper()
	taskDir := filepath.Join(rolesDir, name, "tasks")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("mkdir role %s: %v", name, err)
	}
	content := "- name: " + name + " task\n  command:\n    cmd: echo " + name + "\n"
	if err := os.WriteFile(filepath.Join(taskDir, "main.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write role %s: %v", name, err)
	}
}

// runRolePlaybook writes the given playbook YAML next to a roles/ dir containing
// the named roles, then runs it with the supplied --roles/--tags/--skip-tags
// filters and returns success + captured output.
func runRolePlaybook(t *testing.T, yamlStr string, roleNames, roles, tags, skipTags []string) (bool, string) {
	t.Helper()

	tmpDir := t.TempDir()
	rolesDir := filepath.Join(tmpDir, "roles")
	for _, name := range roleNames {
		writeRole(t, rolesDir, name)
	}

	pbPath := filepath.Join(tmpDir, "site.yaml")
	if err := os.WriteFile(pbPath, []byte(yamlStr), 0644); err != nil {
		t.Fatalf("write playbook: %v", err)
	}

	pb, err := playbook.ParseRaw([]byte(yamlStr), pbPath)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	buf := &bytes.Buffer{}
	exec := New()
	o := output.New(buf)
	o.SetVerbose(true) // plan hides skipped tasks by default; reveal them for assertions
	exec.Output = o
	exec.AutoApprove = true
	exec.Roles = roles
	exec.Tags = tags
	exec.SkipTags = skipTags

	result, err := exec.Run(context.Background(), pb)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}

	return result.Success, buf.String()
}

const threeRolePlaybook = `
name: Roles filter test
hosts: localhost
gather_facts: false
roles:
  - web
  - db
  - cache
`

// 4.1 --roles runs only the named role's tasks and skips other roles.
func TestRolesFilterRunsOnlyNamedRole(t *testing.T) {
	success, out := runRolePlaybook(t, threeRolePlaybook, []string{"web", "db", "cache"}, []string{"web"}, nil, nil)
	if !success {
		t.Fatalf("expected success. Output:\n%s", out)
	}

	if !strings.Contains(out, "web task") {
		t.Error("expected 'web task' to run")
	}
	if !strings.Contains(out, "db task") || !strings.Contains(out, "cache task") {
		t.Error("expected 'db task' and 'cache task' to appear (as skipped)")
	}
	if !strings.Contains(out, "skipped (role)") {
		t.Error("expected db/cache tasks to be reported as 'skipped (role)'")
	}
}

// 4.2 multiple roles use OR logic; play-level tasks are skipped when --roles is set.
func TestRolesFilterMultipleRolesAndSkipsPlayTasks(t *testing.T) {
	yaml := `
name: Roles + play tasks
hosts: localhost
gather_facts: false
roles:
  - web
  - db
  - cache
tasks:
  - name: Play level task
    command:
      cmd: echo playlevel
`
	success, out := runRolePlaybook(t, yaml, []string{"web", "db", "cache"}, []string{"web", "db"}, nil, nil)
	if !success {
		t.Fatalf("expected success. Output:\n%s", out)
	}

	if !strings.Contains(out, "web task") || !strings.Contains(out, "db task") {
		t.Error("expected both 'web task' and 'db task' to run (OR logic)")
	}
	// Play-level task should be present but skipped by the role filter.
	if !strings.Contains(out, "Play level task") {
		t.Fatalf("expected 'Play level task' to appear in output:\n%s", out)
	}
	idx := strings.Index(out, "Play level task")
	rest := out[idx:]
	end := strings.IndexByte(rest, '\n')
	if end == -1 {
		end = len(rest)
	}
	if !strings.Contains(rest[:end], "skipped") {
		t.Errorf("expected 'Play level task' line to be skipped, got:\n%s", rest[:end])
	}
}

// 4.3 --roles composes with --tags and --skip-tags (AND logic).
func TestRolesFilterComposesWithTags(t *testing.T) {
	yaml := `
name: Roles + tags
hosts: localhost
gather_facts: false
roles:
  - role: web
    tags: [deploy]
  - role: db
    tags: [deploy]
`
	// roles=web AND tags=deploy: only web's task should run; db is filtered by role.
	success, out := runRolePlaybook(t, yaml, []string{"web", "db"}, []string{"web"}, []string{"deploy"}, nil)
	if !success {
		t.Fatalf("expected success. Output:\n%s", out)
	}
	if !strings.Contains(out, "web task") {
		t.Error("expected 'web task' to run (matches role AND tag)")
	}

	// roles=web AND skip-tags=deploy: web matches role but is skipped by tag.
	success2, out2 := runRolePlaybook(t, yaml, []string{"web", "db"}, []string{"web"}, nil, []string{"deploy"})
	if !success2 {
		t.Fatalf("expected success. Output:\n%s", out2)
	}
	if strings.Contains(out2, "web task") && !strings.Contains(out2, "skipped") {
		t.Error("expected 'web task' to be skipped by --skip-tags despite matching role")
	}
}

// 4.4 unknown role name matches nothing and does not error.
func TestRolesFilterUnknownRoleMatchesNothing(t *testing.T) {
	success, out := runRolePlaybook(t, threeRolePlaybook, []string{"web", "db", "cache"}, []string{"nope"}, nil, nil)
	if !success {
		t.Fatalf("expected success (no error) for unknown role. Output:\n%s", out)
	}
	if strings.Contains(out, "web task") && !strings.Contains(out, "skipped (role)") {
		t.Error("expected no role tasks to run for an unknown role name")
	}
}

// 4.5 excluded tasks are reported as skipped in plan/preview output too.
func TestRolesFilterReportsSkippedInPlan(t *testing.T) {
	tmpDir := t.TempDir()
	rolesDir := filepath.Join(tmpDir, "roles")
	for _, name := range []string{"web", "db"} {
		writeRole(t, rolesDir, name)
	}
	pbPath := filepath.Join(tmpDir, "site.yaml")
	yamlStr := `
name: Roles plan test
hosts: localhost
gather_facts: false
roles:
  - web
  - db
`
	if err := os.WriteFile(pbPath, []byte(yamlStr), 0644); err != nil {
		t.Fatalf("write playbook: %v", err)
	}
	pb, err := playbook.ParseRaw([]byte(yamlStr), pbPath)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	buf := &bytes.Buffer{}
	exec := New()
	o := output.New(buf)
	o.SetVerbose(true) // plan hides skipped tasks by default; reveal them for assertions
	exec.Output = o
	exec.DryRun = true
	exec.Roles = []string{"web"}

	if _, err := exec.Run(context.Background(), pb); err != nil {
		t.Fatalf("run error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "db task") {
		t.Errorf("expected 'db task' to appear in plan as skipped:\n%s", out)
	}
	if !strings.Contains(out, "skipped (role)") {
		t.Errorf("expected 'skipped (role)' reason in plan output:\n%s", out)
	}
}
