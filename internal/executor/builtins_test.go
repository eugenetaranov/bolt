package executor

import (
	"strings"
	"testing"

	_ "github.com/tackhq/tack/internal/module/command" // register command for handler tests
)

func TestSetFact_UsableByLaterTask(t *testing.T) {
	yaml := `
- name: set_fact play
  hosts: localhost
  connection: local
  tasks:
    - set_fact:
        greeting: "hello"
        count: 3
    - assert:
        that:
          - "greeting == 'hello'"
          - "count == 3"
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success, output:\n%s", out)
	}
}

func TestFail_AbortsWithMessage(t *testing.T) {
	yaml := `
- name: fail play
  hosts: localhost
  connection: local
  tasks:
    - fail:
        msg: "stop right there"
      when: "1 == 1"
`
	ok, out := runAssertPlaybook(t, yaml)
	if ok {
		t.Fatalf("expected failure, output:\n%s", out)
	}
	if !strings.Contains(out, "stop right there") {
		t.Errorf("expected fail message in output:\n%s", out)
	}
}

func TestFail_SkippedByWhen(t *testing.T) {
	yaml := `
- name: fail skipped
  hosts: localhost
  connection: local
  tasks:
    - fail:
        msg: "should not fire"
      when: "1 == 2"
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success (fail gated off), output:\n%s", out)
	}
	if strings.Contains(out, "should not fire") {
		t.Errorf("fail message should not appear:\n%s", out)
	}
}

func TestDebug_InterpolatesMessage(t *testing.T) {
	yaml := `
- name: debug play
  hosts: localhost
  connection: local
  tasks:
    - set_fact:
        who: "world"
    - debug:
        msg: "hi {{ who }}"
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success, output:\n%s", out)
	}
	if !strings.Contains(out, "hi world") {
		t.Errorf("expected interpolated debug message, output:\n%s", out)
	}
}

func TestMetaFlushHandlers_RunsHandlersMidPlay(t *testing.T) {
	yaml := `
- name: flush play
  hosts: localhost
  connection: local
  tasks:
    - name: trigger
      command:
        cmd: "true"
      notify: say hello
    - meta: flush_handlers
    - name: after marker
      debug:
        msg: "AFTER_MARKER"
  handlers:
    - name: say hello
      debug:
        msg: "HANDLER_RAN"
`
	ok, out := runAssertPlaybook(t, yaml)
	if !ok {
		t.Fatalf("expected success, output:\n%s", out)
	}
	hi := strings.Index(out, "HANDLER_RAN")
	ai := strings.Index(out, "AFTER_MARKER")
	if hi < 0 || ai < 0 {
		t.Fatalf("expected both handler and after markers, output:\n%s", out)
	}
	if hi > ai {
		t.Errorf("flush_handlers should run the handler BEFORE the following task:\n%s", out)
	}
}
