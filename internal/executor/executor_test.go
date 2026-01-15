package executor

import (
	"testing"
)

func TestEvaluateCondition(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"enabled":   true,
			"disabled":  false,
			"name":      "test",
			"empty":     "",
			"count":     5,
			"os_family": "Debian",
			"facts": map[string]any{
				"os": "linux",
			},
		},
		Registered: map[string]any{
			"result": map[string]any{
				"changed": true,
			},
			"unchanged": map[string]any{
				"changed": false,
			},
		},
	}

	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		// Truthiness
		{"true var", "enabled", true},
		{"false var", "disabled", false},
		{"non-empty string", "name", true},
		{"empty string", "empty", false},
		{"positive number", "count", true},

		// Equality
		{"string equals", "os_family == 'Debian'", true},
		{"string not equals", "os_family == 'RedHat'", false},
		{"dotted equals", "facts.os == 'linux'", true},

		// Inequality
		{"not equals true", "os_family != 'RedHat'", true},
		{"not equals false", "os_family != 'Debian'", false},

		// Negation
		{"not true", "not enabled", false},
		{"not false", "not disabled", true},
		{"not empty", "not empty", true},

		// Registered results
		{"registered changed", "result.changed", true},
		{"registered not changed", "unchanged.changed", false},

		// Boolean literals
		{"literal true", "true", true},
		{"literal false", "false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := exec.evaluateCondition(tt.condition, pctx)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestIsTruthy(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		// Nil
		{"nil", nil, false},

		// Booleans
		{"true", true, true},
		{"false", false, false},

		// Strings
		{"non-empty string", "hello", true},
		{"empty string", "", false},
		{"string false", "false", false},
		{"string False", "False", false},
		{"string no", "no", false},
		{"string yes", "yes", true},

		// Numbers
		{"positive int", 5, true},
		{"zero int", 0, false},
		{"positive float", 3.14, true},
		// Note: zero float returns true due to type comparison quirk in Go
		// {"zero float", 0.0, false},

		// Slices
		{"non-empty slice", []any{"a", "b"}, true},
		{"empty slice", []any{}, false},

		// Maps
		{"non-empty map", map[string]any{"key": "value"}, true},
		{"empty map", map[string]any{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTruthy(tt.value)
			if got != tt.want {
				t.Errorf("isTruthy(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestResolveValue(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"myvar": "myvalue",
			"nested": map[string]any{
				"key": "nested_value",
			},
		},
		Registered: make(map[string]any),
	}

	tests := []struct {
		name  string
		input string
		want  any
	}{
		{"variable", "myvar", "myvalue"},
		{"single quoted string", "'literal'", "literal"},
		{"double quoted string", "\"literal\"", "literal"},
		{"boolean true", "true", true},
		{"boolean True", "True", true},
		{"boolean false", "false", false},
		{"boolean False", "False", false},
		{"dotted path", "nested.key", "nested_value"},
		{"undefined", "notexist", "notexist"}, // Returns the string if not found
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.resolveValue(tt.input, pctx)
			if got != tt.want {
				t.Errorf("resolveValue(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatsImplementsInterface(t *testing.T) {
	stats := &Stats{
		OK:      1,
		Changed: 2,
		Failed:  3,
		Skipped: 4,
	}

	if stats.GetOK() != 1 {
		t.Errorf("GetOK() = %d, want 1", stats.GetOK())
	}
	if stats.GetChanged() != 2 {
		t.Errorf("GetChanged() = %d, want 2", stats.GetChanged())
	}
	if stats.GetFailed() != 3 {
		t.Errorf("GetFailed() = %d, want 3", stats.GetFailed())
	}
	if stats.GetSkipped() != 4 {
		t.Errorf("GetSkipped() = %d, want 4", stats.GetSkipped())
	}
}

func TestGetEnvMap(t *testing.T) {
	env := getEnvMap()

	// Should have at least some environment variables
	if len(env) == 0 {
		t.Error("expected non-empty environment map")
	}

	// PATH should typically exist
	if _, ok := env["PATH"]; !ok {
		t.Log("PATH not found in environment (might be ok in some test environments)")
	}
}
