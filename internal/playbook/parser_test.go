package playbook

import (
	"testing"
)

func TestParseRaw(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantPlays int
		wantErr   bool
	}{
		{
			name: "single play",
			yaml: `
name: Test Play
hosts: localhost
tasks:
  - name: Say hello
    command:
      cmd: echo hello
`,
			wantPlays: 1,
			wantErr:   false,
		},
		{
			name: "multiple plays",
			yaml: `
- name: Play 1
  hosts: localhost
  tasks:
    - command:
        cmd: echo one

- name: Play 2
  hosts: localhost
  tasks:
    - command:
        cmd: echo two
`,
			wantPlays: 2,
			wantErr:   false,
		},
		{
			name:    "invalid yaml",
			yaml:    `{{{invalid`,
			wantErr: true,
		},
		{
			name: "play with vars",
			yaml: `
name: Test
hosts: localhost
vars:
  greeting: hello
  count: 5
tasks:
  - command:
      cmd: echo test
`,
			wantPlays: 1,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := ParseRaw([]byte(tt.yaml), "test.yaml")
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(pb.Plays) != tt.wantPlays {
				t.Errorf("expected %d plays, got %d", tt.wantPlays, len(pb.Plays))
			}
		})
	}
}

func TestParseRawTask(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantModule string
		wantParams map[string]any
	}{
		{
			name: "command with map params",
			yaml: `
hosts: localhost
tasks:
  - name: Test
    command:
      cmd: echo hello
`,
			wantModule: "command",
			wantParams: map[string]any{"cmd": "echo hello"},
		},
		{
			name: "file module",
			yaml: `
hosts: localhost
tasks:
  - file:
      path: /tmp/test
      state: directory
`,
			wantModule: "file",
			wantParams: map[string]any{"path": "/tmp/test", "state": "directory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := ParseRaw([]byte(tt.yaml), "test.yaml")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if len(pb.Plays) == 0 || len(pb.Plays[0].Tasks) == 0 {
				t.Fatal("no tasks parsed")
			}
			task := pb.Plays[0].Tasks[0]
			if task.Module != tt.wantModule {
				t.Errorf("expected module %q, got %q", tt.wantModule, task.Module)
			}
			for k, v := range tt.wantParams {
				if task.Params[k] != v {
					t.Errorf("param %q: expected %v, got %v", k, v, task.Params[k])
				}
			}
		})
	}
}

func TestParseNotify(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantNotify []string
	}{
		{
			name: "single notify as string",
			yaml: `
hosts: localhost
tasks:
  - command:
      cmd: echo test
    notify: restart service
`,
			wantNotify: []string{"restart service"},
		},
		{
			name: "multiple notify as list",
			yaml: `
hosts: localhost
tasks:
  - command:
      cmd: echo test
    notify:
      - restart nginx
      - reload config
`,
			wantNotify: []string{"restart nginx", "reload config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := ParseRaw([]byte(tt.yaml), "test.yaml")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			task := pb.Plays[0].Tasks[0]
			if len(task.Notify) != len(tt.wantNotify) {
				t.Errorf("expected %d notify handlers, got %d", len(tt.wantNotify), len(task.Notify))
				return
			}
			for i, n := range tt.wantNotify {
				if task.Notify[i] != n {
					t.Errorf("notify[%d]: expected %q, got %q", i, n, task.Notify[i])
				}
			}
		})
	}
}

func TestParseLoop(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantLoop int
	}{
		{
			name: "loop keyword",
			yaml: `
hosts: localhost
tasks:
  - command:
      cmd: echo {{ item }}
    loop:
      - one
      - two
      - three
`,
			wantLoop: 3,
		},
		{
			name: "with_items keyword",
			yaml: `
hosts: localhost
tasks:
  - command:
      cmd: echo {{ item }}
    with_items:
      - a
      - b
`,
			wantLoop: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb, err := ParseRaw([]byte(tt.yaml), "test.yaml")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			task := pb.Plays[0].Tasks[0]
			if len(task.Loop) != tt.wantLoop {
				t.Errorf("expected loop length %d, got %d", tt.wantLoop, len(task.Loop))
			}
		})
	}
}

func TestExpandShorthand(t *testing.T) {
	tests := []struct {
		name       string
		task       *Task
		wantParams map[string]any
	}{
		{
			name: "key=value format",
			task: &Task{
				Module: "apt",
				Params: map[string]any{"_raw": "name=nginx state=present"},
			},
			wantParams: map[string]any{"name": "nginx", "state": "present"},
		},
		{
			name: "command single arg",
			task: &Task{
				Module: "command",
				Params: map[string]any{"_raw": "echo hello world"},
			},
			wantParams: map[string]any{"cmd": "echo hello world"},
		},
		{
			name: "file single arg",
			task: &Task{
				Module: "file",
				Params: map[string]any{"_raw": "/tmp/test"},
			},
			wantParams: map[string]any{"path": "/tmp/test"},
		},
		{
			name: "no expansion needed",
			task: &Task{
				Module: "command",
				Params: map[string]any{"cmd": "echo test"},
			},
			wantParams: map[string]any{"cmd": "echo test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ExpandShorthand(tt.task)
			for k, v := range tt.wantParams {
				if tt.task.Params[k] != v {
					t.Errorf("param %q: expected %v, got %v", k, v, tt.task.Params[k])
				}
			}
		})
	}
}

func TestParseHandlers(t *testing.T) {
	yaml := `
hosts: localhost
tasks:
  - name: Install nginx
    command:
      cmd: apt-get install nginx
    notify: restart nginx

handlers:
  - name: restart nginx
    command:
      cmd: systemctl restart nginx
`
	pb, err := ParseRaw([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(pb.Plays[0].Handlers) != 1 {
		t.Errorf("expected 1 handler, got %d", len(pb.Plays[0].Handlers))
	}

	handler := pb.Plays[0].Handlers[0]
	if handler.Name != "restart nginx" {
		t.Errorf("expected handler name 'restart nginx', got %q", handler.Name)
	}
}
