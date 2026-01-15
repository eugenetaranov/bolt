package playbook

import (
	"testing"
)

func TestPlayValidate(t *testing.T) {
	tests := []struct {
		name    string
		play    Play
		wantErr bool
		errMsg  string
	}{
		{
			name:    "missing hosts",
			play:    Play{},
			wantErr: true,
			errMsg:  "missing required 'hosts' field",
		},
		{
			name: "valid local connection",
			play: Play{
				Hosts:      "localhost",
				Connection: "local",
				Tasks:      []*Task{{Module: "command", Params: map[string]any{"cmd": "echo"}}},
			},
			wantErr: false,
		},
		{
			name: "valid docker connection",
			play: Play{
				Hosts:      "my-container",
				Connection: "docker",
				Tasks:      []*Task{{Module: "command", Params: map[string]any{"cmd": "echo"}}},
			},
			wantErr: false,
		},
		{
			name: "invalid connection type",
			play: Play{
				Hosts:      "localhost",
				Connection: "invalid",
				Tasks:      []*Task{{Module: "command", Params: map[string]any{"cmd": "echo"}}},
			},
			wantErr: true,
			errMsg:  "invalid connection type",
		},
		{
			name: "task with no module",
			play: Play{
				Hosts: "localhost",
				Tasks: []*Task{{Name: "bad task"}},
			},
			wantErr: true,
			errMsg:  "no module specified",
		},
		{
			name: "handler without name",
			play: Play{
				Hosts:    "localhost",
				Tasks:    []*Task{{Module: "command", Params: map[string]any{"cmd": "echo"}}},
				Handlers: []*Task{{Module: "command", Params: map[string]any{"cmd": "echo"}}},
			},
			wantErr: true,
			errMsg:  "handlers must have a name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.play.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestTaskValidate(t *testing.T) {
	tests := []struct {
		name    string
		task    Task
		wantErr bool
		errMsg  string
	}{
		{
			name:    "missing module",
			task:    Task{Name: "test"},
			wantErr: true,
			errMsg:  "no module specified",
		},
		{
			name:    "negative retries",
			task:    Task{Module: "command", Retries: -1},
			wantErr: true,
			errMsg:  "retries cannot be negative",
		},
		{
			name:    "negative delay",
			task:    Task{Module: "command", Delay: -1},
			wantErr: true,
			errMsg:  "delay cannot be negative",
		},
		{
			name:    "valid task",
			task:    Task{Module: "command", Retries: 3, Delay: 5},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.task.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPlayShouldGatherFacts(t *testing.T) {
	t.Run("default is true", func(t *testing.T) {
		p := &Play{}
		if !p.ShouldGatherFacts() {
			t.Error("expected default to be true")
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		val := true
		p := &Play{GatherFacts: &val}
		if !p.ShouldGatherFacts() {
			t.Error("expected true")
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		val := false
		p := &Play{GatherFacts: &val}
		if p.ShouldGatherFacts() {
			t.Error("expected false")
		}
	})
}

func TestPlayGetConnection(t *testing.T) {
	t.Run("default is local", func(t *testing.T) {
		p := &Play{}
		if got := p.GetConnection(); got != "local" {
			t.Errorf("expected 'local', got %q", got)
		}
	})

	t.Run("explicit connection", func(t *testing.T) {
		p := &Play{Connection: "docker"}
		if got := p.GetConnection(); got != "docker" {
			t.Errorf("expected 'docker', got %q", got)
		}
	})
}

func TestPlayGetBecomeUser(t *testing.T) {
	t.Run("default is root", func(t *testing.T) {
		p := &Play{}
		if got := p.GetBecomeUser(); got != "root" {
			t.Errorf("expected 'root', got %q", got)
		}
	})

	t.Run("explicit user", func(t *testing.T) {
		p := &Play{BecomeUser: "admin"}
		if got := p.GetBecomeUser(); got != "admin" {
			t.Errorf("expected 'admin', got %q", got)
		}
	})
}

func TestTaskGetLoopVar(t *testing.T) {
	t.Run("default is item", func(t *testing.T) {
		task := &Task{}
		if got := task.GetLoopVar(); got != "item" {
			t.Errorf("expected 'item', got %q", got)
		}
	})

	t.Run("custom loop var", func(t *testing.T) {
		task := &Task{LoopVar: "pkg"}
		if got := task.GetLoopVar(); got != "pkg" {
			t.Errorf("expected 'pkg', got %q", got)
		}
	})
}

func TestTaskShouldBecome(t *testing.T) {
	t.Run("inherits from play", func(t *testing.T) {
		task := &Task{}
		if task.ShouldBecome(true) != true {
			t.Error("expected to inherit true from play")
		}
		if task.ShouldBecome(false) != false {
			t.Error("expected to inherit false from play")
		}
	})

	t.Run("task overrides play", func(t *testing.T) {
		val := false
		task := &Task{Become: &val}
		if task.ShouldBecome(true) != false {
			t.Error("expected task to override play")
		}
	})
}

func TestTaskString(t *testing.T) {
	t.Run("with name", func(t *testing.T) {
		task := &Task{Name: "Install packages", Module: "apt"}
		if got := task.String(); got != "Install packages" {
			t.Errorf("expected 'Install packages', got %q", got)
		}
	})

	t.Run("without name", func(t *testing.T) {
		task := &Task{Module: "command", Params: map[string]any{"cmd": "echo hello"}}
		got := task.String()
		if got == "" {
			t.Error("expected non-empty string")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
