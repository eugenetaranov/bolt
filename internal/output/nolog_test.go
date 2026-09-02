package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Secret-looking param keys must be redacted in the text plan even without
// no_log. (A secret inlined into a shown value like `cmd` is not key-detectable
// — that case is what no_log exists for.)
func TestFormatTaskParams_RedactsSensitiveKeys(t *testing.T) {
	// Generic module path exposes all keys — secret values must be redacted there.
	lines := formatTaskParams("mymod", map[string]any{
		"password":   "hunter2",
		"api_token":  "abc123",
		"secret_key": "zzz",
		"host":       "db1",
	}, false)
	joined := strings.Join(lines, "\n")
	for _, secret := range []string{"hunter2", "abc123", "zzz"} {
		if strings.Contains(joined, secret) {
			t.Errorf("secret %q leaked into params output: %q", secret, joined)
		}
	}
	if !strings.Contains(joined, redactedPlaceholder) {
		t.Errorf("expected %q in output, got %q", redactedPlaceholder, joined)
	}
	if !strings.Contains(joined, "host: db1") {
		t.Errorf("non-secret value should remain, got %q", joined)
	}
}

// no_log suppresses all params.
func TestFormatTaskParams_NoLogSuppressesEverything(t *testing.T) {
	lines := formatTaskParams("command", map[string]any{"cmd": "echo secret"}, true)
	if len(lines) != 1 || lines[0] != NoLogPlaceholder {
		t.Fatalf("expected only the no_log placeholder, got %v", lines)
	}
}

// The JSON plan event redacts params and flags no_log.
func TestJSONPlan_NoLogRedaction(t *testing.T) {
	buf := &bytes.Buffer{}
	j := NewJSONEmitter(buf, buf)
	j.diff = true
	j.DisplayPlan([]PlannedTask{{
		Name:       "deploy",
		Module:     "command",
		Status:     "will_run",
		Params:     map[string]any{"cmd": "run --token=abc123"},
		NewContent: "secret-content",
		NoLog:      true,
	}}, false)

	var ev map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, buf.String())
	}
	if ev["no_log"] != true {
		t.Errorf("expected no_log=true, got %v", ev["no_log"])
	}
	if _, ok := ev["params"]; ok {
		t.Errorf("params must be omitted for no_log task, got %v", ev["params"])
	}
	if strings.Contains(buf.String(), "abc123") || strings.Contains(buf.String(), "secret-content") {
		t.Errorf("secret leaked into JSON: %s", buf.String())
	}
}

// The JSON plan event redacts secret-looking keys even without no_log.
func TestJSONPlan_SensitiveKeyRedaction(t *testing.T) {
	buf := &bytes.Buffer{}
	j := NewJSONEmitter(buf, buf)
	j.DisplayPlan([]PlannedTask{{
		Name:   "cfg",
		Module: "mymod",
		Status: "will_run",
		Params: map[string]any{"host": "db1", "password": "hunter2"},
	}}, false)
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("password leaked into JSON: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "db1") {
		t.Errorf("non-secret value should remain: %s", buf.String())
	}
}
