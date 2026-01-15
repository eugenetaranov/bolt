package executor

import (
	"testing"
)

func TestInterpolateString(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"name":     "world",
			"greeting": "hello",
			"count":    42,
			"env": map[string]string{
				"HOME": "/home/user",
				"USER": "testuser",
			},
			"facts": map[string]any{
				"os":        "linux",
				"os_family": "Debian",
			},
		},
		Registered: make(map[string]any),
	}

	tests := []struct {
		name   string
		input  string
		want   any
		errMsg string
	}{
		{
			name:  "simple variable",
			input: "{{ name }}",
			want:  "world",
		},
		{
			name:  "variable in text",
			input: "Hello, {{ name }}!",
			want:  "Hello, world!",
		},
		{
			name:  "multiple variables",
			input: "{{ greeting }}, {{ name }}!",
			want:  "hello, world!",
		},
		{
			name:  "dotted path - env",
			input: "{{ env.HOME }}",
			want:  "/home/user",
		},
		{
			name:  "dotted path - facts",
			input: "{{ facts.os_family }}",
			want:  "Debian",
		},
		{
			name:  "integer variable",
			input: "{{ count }}",
			want:  42,
		},
		{
			name:  "undefined variable",
			input: "{{ undefined }}",
			want:  nil,
		},
		{
			name:  "no variables",
			input: "plain text",
			want:  "plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := exec.interpolateString(tt.input, pctx)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("expected %v (%T), got %v (%T)", tt.want, tt.want, got, got)
			}
		})
	}
}

func TestLookupVariable(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"simple": "value",
			"nested": map[string]any{
				"key": "nested_value",
				"deep": map[string]any{
					"value": "deep_value",
				},
			},
		},
		Registered: map[string]any{
			"result": map[string]any{
				"changed": true,
				"data":    "test",
			},
		},
	}

	tests := []struct {
		name string
		key  string
		want any
	}{
		{"simple var", "simple", "value"},
		{"nested var", "nested.key", "nested_value"},
		{"deep nested", "nested.deep.value", "deep_value"},
		{"registered var", "result", map[string]any{"changed": true, "data": "test"}},
		{"undefined", "notexist", nil},
		{"undefined nested", "nested.notexist", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.lookupVariable(tt.key, pctx)
			if tt.want == nil && got != nil {
				t.Errorf("expected nil, got %v", got)
			} else if tt.want != nil {
				switch w := tt.want.(type) {
				case string:
					if got != w {
						t.Errorf("expected %q, got %v", w, got)
					}
				case map[string]any:
					// Just check it's not nil for maps
					if got == nil {
						t.Error("expected map, got nil")
					}
				}
			}
		})
	}
}

func TestApplyFilter(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"name":      "Hello World",
			"empty":     "",
			"items":     []any{"a", "b", "c"},
			"number":    "42",
			"trimmed":   "  spaces  ",
			"undefined": nil,
		},
		Registered: make(map[string]any),
	}

	tests := []struct {
		name    string
		varName string
		filter  string
		want    any
	}{
		{"default with value", "name", "default('fallback')", "Hello World"},
		{"default with empty", "empty", "default('fallback')", "fallback"},
		{"default with undefined", "notexist", "default('fallback')", "fallback"},
		{"lower", "name", "lower", "hello world"},
		{"upper", "name", "upper", "HELLO WORLD"},
		{"trim", "trimmed", "trim", "spaces"},
		{"first", "items", "first", "a"},
		{"last", "items", "last", "c"},
		{"length string", "name", "length", 11},
		{"length array", "items", "length", 3},
		{"join default", "items", "join", "a,b,c"},
		{"join custom", "items", "join(' ')", "a b c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := exec.applyFilter(tt.varName, tt.filter, pctx)
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

func TestApplyFilterUnknown(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars:       map[string]any{"x": "test"},
		Registered: make(map[string]any),
	}

	_, err := exec.applyFilter("x", "unknownfilter", pctx)
	if err == nil {
		t.Error("expected error for unknown filter")
	}
}

func TestInterpolateParams(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"pkg":  "nginx",
			"path": "/var/www",
		},
		Registered: make(map[string]any),
	}

	params := map[string]any{
		"name":  "{{ pkg }}",
		"dest":  "{{ path }}/html",
		"count": 5,
	}

	result, err := exec.interpolateParams(params, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["name"] != "nginx" {
		t.Errorf("name: expected 'nginx', got %v", result["name"])
	}
	if result["dest"] != "/var/www/html" {
		t.Errorf("dest: expected '/var/www/html', got %v", result["dest"])
	}
	if result["count"] != 5 {
		t.Errorf("count: expected 5, got %v", result["count"])
	}
}

func TestInterpolateNestedParams(t *testing.T) {
	exec := New()
	pctx := &PlayContext{
		Vars: map[string]any{
			"user": "admin",
		},
		Registered: make(map[string]any),
	}

	params := map[string]any{
		"config": map[string]any{
			"owner": "{{ user }}",
			"mode":  "0644",
		},
		"items": []any{"{{ user }}", "guest"},
	}

	result, err := exec.interpolateParams(params, pctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	config := result["config"].(map[string]any)
	if config["owner"] != "admin" {
		t.Errorf("config.owner: expected 'admin', got %v", config["owner"])
	}

	items := result["items"].([]any)
	if items[0] != "admin" {
		t.Errorf("items[0]: expected 'admin', got %v", items[0])
	}
}
