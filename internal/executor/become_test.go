package executor

import (
	"testing"

	"github.com/tackhq/tack/internal/playbook"
)

func TestApplyBecome_PlayDefaultsTaskOverrideAndRestore(t *testing.T) {
	e := New()
	conn := &cwConn{}
	pctx := &PlayContext{
		Play: &playbook.Play{
			Sudo:         true,
			BecomeUser:   "postgres",
			BecomeMethod: "sudo",
			SudoPassword: "pw",
		},
		Connector: conn,
		Vars:      map[string]any{},
	}

	// A task with no override inherits the play's become settings.
	restore := e.applyBecome(pctx, &playbook.Task{})
	if !conn.become.Enabled || conn.become.User != "postgres" || conn.become.Method != "sudo" || conn.become.Password != "pw" {
		t.Fatalf("play become not applied: %+v", conn.become)
	}
	restore()

	// A task-level override wins.
	restore = e.applyBecome(pctx, &playbook.Task{BecomeUser: "deploy", BecomeMethod: "doas"})
	if conn.become.User != "deploy" || conn.become.Method != "doas" {
		t.Fatalf("task override not applied: %+v", conn.become)
	}
	// Restoring reverts to the play defaults.
	restore()
	if conn.become.User != "postgres" || conn.become.Method != "sudo" {
		t.Fatalf("restore did not revert to play defaults: %+v", conn.become)
	}
}
