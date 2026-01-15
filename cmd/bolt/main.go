// Package main is the entrypoint for the bolt CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	// Import modules to register them
	_ "github.com/eugenetaranov/bolt/internal/module/apt"
	_ "github.com/eugenetaranov/bolt/internal/module/brew"
	_ "github.com/eugenetaranov/bolt/internal/module/command"
	_ "github.com/eugenetaranov/bolt/internal/module/copy"
	_ "github.com/eugenetaranov/bolt/internal/module/file"

	"github.com/eugenetaranov/bolt/internal/executor"
	"github.com/eugenetaranov/bolt/internal/module"
	"github.com/eugenetaranov/bolt/internal/playbook"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Global flags
var (
	debug   bool
	dryRun  bool
	noColor bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "bolt",
	Short: "Bolt - System bootstrapping and configuration management",
	Long: `Bolt is a Go-based configuration management tool inspired by Ansible.
It helps you bootstrap and configure macOS and Linux systems using
simple YAML playbooks.

Supports local execution, SSH, and AWS SSM connectors.`,
	Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug output with detailed task information")
	rootCmd.PersistentFlags().BoolVarP(&dryRun, "dry-run", "n", false, "Show what would be done without making changes")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")

	// Add subcommands
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(modulesCmd)
}

// runCmd executes a playbook
var runCmd = &cobra.Command{
	Use:   "run <playbook.yaml>",
	Short: "Run a playbook",
	Long: `Execute a playbook against the specified hosts.

Examples:
  bolt run setup.yaml
  bolt run setup.yaml --debug
  bolt run setup.yaml --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runPlaybook,
}

func init() {
	// Run-specific flags can be added here
	runCmd.Flags().StringP("inventory", "i", "", "Inventory file (not yet implemented)")
	runCmd.Flags().StringSliceP("extra-vars", "e", nil, "Extra variables (key=value)")
	runCmd.Flags().StringSlice("tags", nil, "Only run tasks with these tags")
	runCmd.Flags().StringSlice("skip-tags", nil, "Skip tasks with these tags")
	runCmd.Flags().IntP("forks", "f", 1, "Number of parallel processes (not yet implemented)")
}

func runPlaybook(cmd *cobra.Command, args []string) error {
	playbookPath := args[0]

	// Check if file exists
	if _, err := os.Stat(playbookPath); os.IsNotExist(err) {
		return fmt.Errorf("playbook not found: %s", playbookPath)
	}

	// Parse playbook
	pb, err := playbook.ParseFileRaw(playbookPath)
	if err != nil {
		return fmt.Errorf("failed to parse playbook: %w", err)
	}

	// Create executor
	exec := executor.New()
	exec.Debug = debug
	exec.DryRun = dryRun
	exec.Output.SetColor(!noColor)
	exec.Output.SetDebug(debug)

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nInterrupted, cleaning up...")
		cancel()
	}()

	// Run playbook
	result, err := exec.Run(ctx, pb)
	if err != nil {
		return err
	}

	if !result.Success {
		os.Exit(1)
	}

	return nil
}

// validateCmd validates a playbook without running it
var validateCmd = &cobra.Command{
	Use:   "validate <playbook.yaml> [playbook2.yaml ...]",
	Short: "Validate one or more playbooks",
	Long: `Parse and validate playbooks without executing them.

This checks for:
  - Valid YAML syntax
  - Required fields (hosts, tasks)
  - Valid module names
  - Task structure

Examples:
  bolt validate setup.yaml
  bolt validate *.yaml`,
	Args: cobra.MinimumNArgs(1),
	RunE: validatePlaybooks,
}

func validatePlaybooks(cmd *cobra.Command, args []string) error {
	var hasErrors bool

	for _, playbookPath := range args {
		if err := validatePlaybook(playbookPath); err != nil {
			fmt.Printf("FAIL: %s - %v\n", playbookPath, err)
			hasErrors = true
		} else {
			fmt.Printf("OK: %s\n", playbookPath)
		}
	}

	if hasErrors {
		return fmt.Errorf("one or more playbooks failed validation")
	}

	fmt.Printf("\nAll %d playbook(s) valid.\n", len(args))
	return nil
}

func validatePlaybook(playbookPath string) error {
	// Check if file exists
	if _, err := os.Stat(playbookPath); os.IsNotExist(err) {
		return fmt.Errorf("not found")
	}

	// Parse playbook
	pb, err := playbook.ParseFileRaw(playbookPath)
	if err != nil {
		return err
	}

	// Validate modules exist
	var errors []string
	for _, play := range pb.Plays {
		for _, task := range play.Tasks {
			playbook.ExpandShorthand(task)
			if err := playbook.ResolveModule(task); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", task.String(), err))
			}
		}
		for _, handler := range play.Handlers {
			playbook.ExpandShorthand(handler)
			if err := playbook.ResolveModule(handler); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", handler.String(), err))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("%d error(s): %s", len(errors), errors[0])
	}

	return nil
}

// modulesCmd lists available modules
var modulesCmd = &cobra.Command{
	Use:   "modules",
	Short: "List available modules",
	Long:  `Display a list of all available modules that can be used in playbooks.`,
	Run: func(cmd *cobra.Command, args []string) {
		modules := module.List()
		if len(modules) == 0 {
			fmt.Println("No modules registered.")
			return
		}

		fmt.Println("Available modules:")
		fmt.Println()
		for _, name := range modules {
			fmt.Printf("  - %s\n", name)
		}
		fmt.Println()
		fmt.Printf("Total: %d modules\n", len(modules))
	},
}
