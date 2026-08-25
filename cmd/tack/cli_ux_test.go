package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tackhq/tack/internal/executor"
	"github.com/tackhq/tack/internal/module"
)

func TestForksFlag_Present(t *testing.T) {
	// The --forks flag should exist on the run command for parallel host execution
	flag := runCmd.Flags().Lookup("forks")
	if flag == nil {
		t.Fatal("expected --forks flag on run command")
	}
	if flag.DefValue != "1" {
		t.Fatalf("expected --forks default to be 1, got %s", flag.DefValue)
	}
	// Check the shorthand
	flag = runCmd.Flags().ShorthandLookup("f")
	if flag == nil {
		t.Fatal("expected -f shorthand on run command")
	}
}

func TestCheckFlag_IsGlobalAlias(t *testing.T) {
	// --check should be a persistent flag on root, not just on run
	flag := rootCmd.PersistentFlags().Lookup("check")
	if flag == nil {
		t.Fatal("expected --check to be a persistent flag on root command")
	}
	// --dry-run should also be persistent
	dryRunFlag := rootCmd.PersistentFlags().Lookup("dry-run")
	if dryRunFlag == nil {
		t.Fatal("expected --dry-run to be a persistent flag on root command")
	}
	// --check should be inherited from root, not defined locally on run
	if runCmd.LocalFlags().Lookup("check") != nil {
		t.Fatal("--check should not be a local flag on run command")
	}
}

func TestModuleCmd_UnknownModule(t *testing.T) {
	moduleCmd.SetArgs([]string{"nonexistent"})
	err := moduleCmd.RunE(moduleCmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown module")
	}
	if !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("expected 'unknown module' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Available modules") {
		t.Fatalf("expected error to list available modules, got: %v", err)
	}
}

func TestModuleCmd_KnownModule(t *testing.T) {
	// Should not error for a known module
	err := moduleCmd.RunE(moduleCmd, []string{"apt"})
	if err != nil {
		t.Fatalf("unexpected error for apt module: %v", err)
	}
}

func TestDescriber_AptImplements(t *testing.T) {
	mod := module.Get("apt")
	if mod == nil {
		t.Fatal("apt module not registered")
	}
	desc, ok := mod.(module.Describer)
	if !ok {
		t.Fatal("apt module does not implement Describer")
	}
	if desc.Description() == "" {
		t.Fatal("apt description is empty")
	}
	params := desc.Parameters()
	if len(params) == 0 {
		t.Fatal("apt parameters is empty")
	}
}

func TestDescriber_YumImplements(t *testing.T) {
	mod := module.Get("yum")
	if mod == nil {
		t.Fatal("yum module not registered")
	}
	desc, ok := mod.(module.Describer)
	if !ok {
		t.Fatal("yum module does not implement Describer")
	}
	if desc.Description() == "" {
		t.Fatal("yum description is empty")
	}
}

func TestDescriber_FileImplements(t *testing.T) {
	mod := module.Get("file")
	if mod == nil {
		t.Fatal("file module not registered")
	}
	desc, ok := mod.(module.Describer)
	if !ok {
		t.Fatal("file module does not implement Describer")
	}
	if desc.Description() == "" {
		t.Fatal("file description is empty")
	}
}

func TestDescriber_CommandImplements(t *testing.T) {
	mod := module.Get("command")
	if mod == nil {
		t.Fatal("command module not registered")
	}
	if _, ok := mod.(module.Describer); !ok {
		t.Fatal("command module does not implement Describer")
	}
}

// TestAllModulesDocumented guarantees every registered module exposes both
// documentation (Describer) and a usage example (Exampler) so that
// `tack module <name>` always shows a schema and a sample.
func TestAllModulesDocumented(t *testing.T) {
	for _, name := range module.List() {
		mod := module.Get(name)
		if _, ok := mod.(module.Describer); !ok {
			t.Errorf("module %q does not implement Describer", name)
		}
		ex, ok := mod.(module.Exampler)
		if !ok {
			t.Errorf("module %q does not implement Exampler", name)
			continue
		}
		sample := strings.TrimSpace(ex.Example())
		if sample == "" {
			t.Errorf("module %q returns an empty Example()", name)
		} else if !strings.HasPrefix(sample, "- ") {
			t.Errorf("module %q Example() should be a YAML task list item starting with '- ', got: %q", name, sample)
		}
	}
}

func TestAutoApproveFlags(t *testing.T) {
	// -a is the primary shorthand for --auto-approve.
	if f := runCmd.Flags().Lookup("auto-approve"); f == nil {
		t.Fatal("expected --auto-approve flag")
	} else if f.Shorthand != "a" {
		t.Errorf("expected -a shorthand for --auto-approve, got %q", f.Shorthand)
	}
	// -y/--yes remains as a backward-compatible alias.
	if y := runCmd.Flags().Lookup("yes"); y == nil {
		t.Fatal("expected --yes alias flag")
	} else if y.Shorthand != "y" {
		t.Errorf("expected -y shorthand for --yes, got %q", y.Shorthand)
	}
	if runCmd.Flags().ShorthandLookup("a") == nil {
		t.Error("-a should be resolvable")
	}
	if runCmd.Flags().ShorthandLookup("y") == nil {
		t.Error("-y should be resolvable")
	}
}

func TestConfigureSudoPrompt(t *testing.T) {
	t.Setenv("TACK_SUDO_NO_PROMPT", "") // ensure env opt-out is off

	newCmd := func(args ...string) *cobra.Command {
		c := &cobra.Command{}
		addConnectionFlags(c)
		if err := c.ParseFlags(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return c
	}
	resolve := func(cmd *cobra.Command, autoApprove, tty bool) (requested, noPrompt bool) {
		e := &executor.Executor{}
		configureSudoPrompt(e, cmd, autoApprove, tty)
		return e.SudoPromptRequested, e.SudoNoPrompt
	}

	// THE REGRESSION: `tack run -sa` on a TTY must still prompt for sudo —
	// --auto-approve skips the plan approval, not the sudo password prompt.
	if req, noPrompt := resolve(newCmd("-s"), true /*autoApprove*/, true /*tty*/); !req || noPrompt {
		t.Errorf("-s with --auto-approve on TTY: requested=%v noPrompt=%v, want true/false", req, noPrompt)
	}

	// auto-approve must be inert: identical result whether or not it's set.
	withAA, _ := resolve(newCmd("-s"), true, true)
	withoutAA, _ := resolve(newCmd("-s"), false, true)
	_, npWith := resolve(newCmd("-s"), true, true)
	_, npWithout := resolve(newCmd("-s"), false, true)
	if withAA != withoutAA || npWith != npWithout {
		t.Errorf("--auto-approve changed sudo-prompt decision (with=%v/%v without=%v/%v)", withAA, npWith, withoutAA, npWithout)
	}

	// Explicit opt-out and non-TTY still skip.
	if _, noPrompt := resolve(newCmd("-s", "--no-sudo-prompt"), true, true); !noPrompt {
		t.Error("--no-sudo-prompt should skip the prompt")
	}
	if _, noPrompt := resolve(newCmd("-s"), false, false /*non-TTY*/); !noPrompt {
		t.Error("non-TTY should skip the prompt")
	}

	// Env opt-out still skips.
	t.Setenv("TACK_SUDO_NO_PROMPT", "1")
	if _, noPrompt := resolve(newCmd("-s"), false, true); !noPrompt {
		t.Error("TACK_SUDO_NO_PROMPT=1 should skip the prompt")
	}
}

func TestColorDisabled(t *testing.T) {
	// Flag wins regardless of env.
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	if !colorDisabled(true) {
		t.Error("--no-color flag should disable color")
	}
	if colorDisabled(false) {
		t.Error("no flag, no env -> color enabled")
	}
	// NO_COLOR set to any non-empty value disables.
	t.Setenv("NO_COLOR", "1")
	if !colorDisabled(false) {
		t.Error("NO_COLOR set should disable color")
	}
	t.Setenv("NO_COLOR", "")
	// CLICOLOR=0 disables; other values do not.
	t.Setenv("CLICOLOR", "0")
	if !colorDisabled(false) {
		t.Error("CLICOLOR=0 should disable color")
	}
	t.Setenv("CLICOLOR", "1")
	if colorDisabled(false) {
		t.Error("CLICOLOR=1 should not disable color")
	}
}

func TestTagsFlag_Shorthand(t *testing.T) {
	f := runCmd.Flags().Lookup("tags")
	if f == nil {
		t.Fatal("expected --tags flag on run command")
	}
	if f.Shorthand != "t" {
		t.Errorf("expected -t shorthand for --tags, got %q", f.Shorthand)
	}
	if runCmd.Flags().ShorthandLookup("t") == nil {
		t.Error("-t should be resolvable")
	}
}
