package executor

import (
	"strings"
	"testing"

	_ "github.com/tackhq/tack/internal/module/command"
)

func TestEnvironment_PlayAndTaskMerged(t *testing.T) {
	yaml := `
- name: env play
  hosts: localhost
  connection: local
  environment:
    GREETING: "hi"
  tasks:
    - name: echo env
      command:
        cmd: 'echo "$GREETING $EXTRA"'
      environment:
        EXTRA: "there"
      register: out
    - assert:
        that:
          - "'hi there' in out.stdout"
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success, output:\n%s", out)
	}
}

func TestUntil_SucceedsWhenConditionMet(t *testing.T) {
	yaml := `
- name: until ok
  hosts: localhost
  connection: local
  tasks:
    - name: quick
      command:
        cmd: 'echo ready'
      until: "'ready' in stdout"
      retries: 2
      delay: 0
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success, output:\n%s", out)
	}
}

func TestUntil_FailsWhenNeverMet(t *testing.T) {
	yaml := `
- name: until fail
  hosts: localhost
  connection: local
  tasks:
    - name: never ready
      command:
        cmd: 'echo nope'
      until: "'ready' in stdout"
      retries: 1
      delay: 0
`
	ok, out := runAssertPlaybook(t, yaml)
	if ok {
		t.Fatalf("expected failure when until never met, output:\n%s", out)
	}
	if !strings.Contains(out, "until condition not met") {
		t.Errorf("expected until failure message, output:\n%s", out)
	}
}
