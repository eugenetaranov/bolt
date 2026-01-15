package module

import (
	"context"
	"testing"

	"github.com/eugenetaranov/bolt/internal/connector"
)

// mockModule is a simple module for testing
type mockModule struct {
	name string
}

func (m *mockModule) Name() string {
	return m.name
}

func (m *mockModule) Run(ctx context.Context, conn connector.Connector, params map[string]any) (*Result, error) {
	return Changed("mock executed"), nil
}

func TestRegisterAndGet(t *testing.T) {
	// Use a unique name to avoid conflicts with other registered modules
	mod := &mockModule{name: "test_mock_module_unique"}

	Register(mod)

	got := Get("test_mock_module_unique")
	if got == nil {
		t.Fatal("expected to find registered module")
	}
	if got.Name() != "test_mock_module_unique" {
		t.Errorf("expected name 'test_mock_module_unique', got %q", got.Name())
	}
}

func TestGetUnknown(t *testing.T) {
	got := Get("nonexistent_module_xyz")
	if got != nil {
		t.Errorf("expected nil for unknown module, got %v", got)
	}
}

func TestList(t *testing.T) {
	// Register another unique module
	Register(&mockModule{name: "test_list_module"})

	names := List()
	if len(names) == 0 {
		t.Error("expected non-empty module list")
	}

	// Check that our test module is in the list
	found := false
	for _, name := range names {
		if name == "test_list_module" {
			found = true
			break
		}
	}
	if !found {
		t.Error("test_list_module not found in List()")
	}
}

func TestResultHelpers(t *testing.T) {
	t.Run("Changed", func(t *testing.T) {
		r := Changed("made changes")
		if !r.Changed {
			t.Error("expected Changed=true")
		}
		if r.Message != "made changes" {
			t.Errorf("expected message 'made changes', got %q", r.Message)
		}
	})

	t.Run("Unchanged", func(t *testing.T) {
		r := Unchanged("already ok")
		if r.Changed {
			t.Error("expected Changed=false")
		}
		if r.Message != "already ok" {
			t.Errorf("expected message 'already ok', got %q", r.Message)
		}
	})

	t.Run("ChangedWithData", func(t *testing.T) {
		data := map[string]any{"key": "value"}
		r := ChangedWithData("with data", data)
		if !r.Changed {
			t.Error("expected Changed=true")
		}
		if r.Data["key"] != "value" {
			t.Errorf("expected data key 'value', got %v", r.Data["key"])
		}
	})
}
