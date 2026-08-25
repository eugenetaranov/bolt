package executor

import (
	"testing"

	"github.com/tackhq/tack/internal/playbook"
)

func boolPtr(b bool) *bool { return &b }

func TestPlayRequiresSudo(t *testing.T) {
	cases := []struct {
		name string
		play *playbook.Play
		want bool
	}{
		{"play-level sudo", &playbook.Play{Sudo: true}, true},
		{"no sudo anywhere", &playbook.Play{Tasks: []*playbook.Task{{Name: "a"}}}, false},
		{
			"task-level sudo",
			&playbook.Play{Tasks: []*playbook.Task{{Name: "a"}, {Name: "b", Sudo: boolPtr(true)}}},
			true,
		},
		{
			"task sudo explicitly false",
			&playbook.Play{Tasks: []*playbook.Task{{Name: "a", Sudo: boolPtr(false)}}},
			false,
		},
		{
			"nested block sudo",
			&playbook.Play{Tasks: []*playbook.Task{{
				Name:  "wrap",
				Block: []*playbook.Task{{Name: "inner", Sudo: boolPtr(true)}},
			}}},
			true,
		},
		{
			"rescue/always sudo",
			&playbook.Play{Tasks: []*playbook.Task{{
				Name:   "wrap",
				Rescue: []*playbook.Task{{Name: "r"}},
				Always: []*playbook.Task{{Name: "cleanup", Sudo: boolPtr(true)}},
			}}},
			true,
		},
		{
			"handler sudo",
			&playbook.Play{Handlers: []*playbook.Task{{Name: "restart", Sudo: boolPtr(true)}}},
			true,
		},
	}
	for _, c := range cases {
		if got := playRequiresSudo(c.play); got != c.want {
			t.Errorf("%s: playRequiresSudo = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeedsSudoPassword_TaskLevel(t *testing.T) {
	// A task-level `sudo: true` (no play-level sudo, no -s) triggers the prompt.
	prompted := false
	e := New()
	e.PromptSudoPassword = func() (string, error) { prompted = true; return "s3cret", nil }
	play := &playbook.Play{Tasks: []*playbook.Task{{Name: "x", Sudo: boolPtr(true)}}}

	if err := e.needsSudoPassword(play); err != nil {
		t.Fatal(err)
	}
	if !prompted {
		t.Error("expected a prompt for task-level sudo")
	}
	if play.SudoPassword != "s3cret" {
		t.Errorf("SudoPassword = %q, want %q", play.SudoPassword, "s3cret")
	}
}

func TestNeedsSudoPassword_NoPrompterErrors(t *testing.T) {
	// Sudo is required but no prompter is wired -> actionable error.
	e := New()
	e.PromptSudoPassword = nil
	if err := e.needsSudoPassword(&playbook.Play{Sudo: true}); err == nil {
		t.Fatal("expected an error when sudo is required but no prompter is available")
	}
}
