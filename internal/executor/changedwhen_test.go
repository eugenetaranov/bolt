package executor

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/tackhq/tack/internal/connector"
	_ "github.com/tackhq/tack/internal/module/command" // register the command module
	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
)

// cwConn is a mock connector that returns a fixed exit code and stdout for the
// command module.
type cwConn struct {
	exitCode int
	stdout   string
}

func (c *cwConn) Connect(context.Context) error { return nil }
func (c *cwConn) Execute(_ context.Context, _ string) (*connector.Result, error) {
	return &connector.Result{Stdout: c.stdout, ExitCode: c.exitCode}, nil
}
func (c *cwConn) Upload(context.Context, io.Reader, string, uint32) error { return nil }
func (c *cwConn) Download(context.Context, string, io.Writer) error       { return nil }
func (c *cwConn) SetSudo(bool, string)                                    {}
func (c *cwConn) Close() error                                            { return nil }
func (c *cwConn) String() string                                          { return "cw://mock" }

func cwPctx(conn connector.Connector) (*PlayContext, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &PlayContext{
		Play:             &playbook.Play{},
		Vars:             map[string]any{},
		Registered:       map[string]any{},
		NotifiedHandlers: map[string]bool{},
		Connector:        conn,
		Output:           output.New(buf),
	}, buf
}

func TestChangedWhenFalse_ForcesUnchanged(t *testing.T) {
	exec := New()
	pctx, _ := cwPctx(&cwConn{exitCode: 0})
	task := &playbook.Task{
		Module:      "command",
		Params:      map[string]any{"cmd": "echo hi"},
		ChangedWhen: "false",
	}

	res, err := exec.runSingleTask(context.Background(), pctx, task, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Changed {
		t.Fatalf("changed_when: false must force unchanged, got changed=%v (status %s)", res.Changed, res.Status)
	}
	if res.Status != "ok" {
		t.Errorf("expected status ok, got %s", res.Status)
	}
}

func TestChangedWhen_ExpressionOnExitCode(t *testing.T) {
	exec := New()
	pctx, _ := cwPctx(&cwConn{exitCode: 0, stdout: "no changes"})
	// Report changed only when stdout mentions "updated".
	task := &playbook.Task{
		Module:      "command",
		Params:      map[string]any{"cmd": "run"},
		ChangedWhen: "'updated' in stdout",
	}
	res, err := exec.runSingleTask(context.Background(), pctx, task, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Changed {
		t.Errorf("expected unchanged when stdout lacks 'updated'")
	}
}

func TestFailedWhenFalse_IgnoresNonZeroExit(t *testing.T) {
	exec := New()
	pctx, _ := cwPctx(&cwConn{exitCode: 2, stdout: "partial"})
	// A non-zero exit normally errors; failed_when:false recovers it.
	task := &playbook.Task{
		Module:     "command",
		Params:     map[string]any{"cmd": "flaky"},
		FailedWhen: "false",
		Register:   "r",
	}
	res, err := exec.runSingleTask(context.Background(), pctx, task, nil)
	if err != nil {
		t.Fatalf("failed_when: false should suppress the error, got: %v", err)
	}
	if res.Status == "failed" {
		t.Errorf("expected non-failed status, got %s", res.Status)
	}
	// The captured exit_code must be visible via register despite the non-zero exit.
	reg, ok := pctx.Vars["r"].(map[string]any)
	if !ok {
		t.Fatalf("register var not stored")
	}
	if reg["exit_code"] != 2 {
		t.Errorf("expected captured exit_code=2, got %v", reg["exit_code"])
	}
}

func TestFailedWhen_ExpressionForcesFailure(t *testing.T) {
	exec := New()
	pctx, _ := cwPctx(&cwConn{exitCode: 0, stdout: "FATAL: disk full"})
	// Exit 0 but output signals failure.
	task := &playbook.Task{
		Module:     "command",
		Params:     map[string]any{"cmd": "check"},
		FailedWhen: "'FATAL' in stdout",
	}
	res, err := exec.runSingleTask(context.Background(), pctx, task, nil)
	if err == nil {
		t.Fatal("expected failed_when to force a failure on FATAL output")
	}
	if res.Status != "failed" {
		t.Errorf("expected failed status, got %s", res.Status)
	}
}
