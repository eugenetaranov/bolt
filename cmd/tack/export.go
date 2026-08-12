package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tackhq/tack/internal/connector/local"
	"github.com/tackhq/tack/internal/export"
	"github.com/tackhq/tack/internal/playbook"
	"github.com/tackhq/tack/internal/source"
)

var exportCmd = &cobra.Command{
	Use:   "export [playbook.yaml]",
	Short: "Compile a playbook into a standalone bash script",
	Long: `Export compiles a playbook into a standalone bash script per host,
resolving variables, templates, conditionals, and loops at export time.

If no playbook argument is given, tack uses site.yaml (or site.yml) in the
current directory.

The emitted script is human-readable, deterministic, and suitable for
security audits, air-gapped environments, and debugging.

Examples:
  tack export --host web01
  tack export setup.yaml --host web01
  tack export setup.yaml --host web01 --output /tmp/web01.sh
  tack export setup.yaml --all-hosts --output /tmp/scripts/
  tack export setup.yaml --host web01 --no-facts
  tack export setup.yaml --host web01 --check-only
  tack export setup.yaml --host web01 -e app_version=2.0
  tack export setup.yaml --host web01 --no-banner-timestamp`,
	Args: cobra.MaximumNArgs(1),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().String("host", "", "Target a single host")
	exportCmd.Flags().Bool("all-hosts", false, "Emit one script per host in inventory")
	exportCmd.Flags().StringP("output", "o", "", "Output path (file for --host, directory for --all-hosts)")
	exportCmd.Flags().Bool("no-facts", false, "Skip fact gathering; leave fact references as sentinels")
	exportCmd.Flags().Bool("check-only", false, "Validate and list unsupported constructs without writing files")
	exportCmd.Flags().Bool("no-banner-timestamp", false, "Omit timestamp from banner for reproducible output")
	exportCmd.Flags().StringSliceP("extra-vars", "e", nil, "Extra variables (key=value)")
	exportCmd.Flags().StringSlice("tags", nil, "Only export tasks with these tags")
	exportCmd.Flags().StringSlice("skip-tags", nil, "Skip tasks with these tags")
	exportCmd.Flags().String("connection", "", "Connection type for fact gathering (local, ssh, ssm, docker)")
	exportCmd.Flags().StringArrayP("inventory", "i", nil, "Inventory source")
}

func runExport(cmd *cobra.Command, args []string) error {
	host, _ := cmd.Flags().GetString("host")
	allHosts, _ := cmd.Flags().GetBool("all-hosts")
	output, _ := cmd.Flags().GetString("output")
	noFacts, _ := cmd.Flags().GetBool("no-facts")
	checkOnly, _ := cmd.Flags().GetBool("check-only")
	noBannerTimestamp, _ := cmd.Flags().GetBool("no-banner-timestamp")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	skipTags, _ := cmd.Flags().GetStringSlice("skip-tags")
	extraVarsSlice, _ := cmd.Flags().GetStringSlice("extra-vars")

	// Validate mutually exclusive flags
	if host != "" && allHosts {
		return fmt.Errorf("--host and --all-hosts are mutually exclusive")
	}
	if allHosts && output == "" {
		return fmt.Errorf("--all-hosts requires --output directory")
	}

	// Parse extra vars
	extraVars := make(map[string]string)
	for _, ev := range extraVarsSlice {
		k, v, ok := strings.Cut(ev, "=")
		if !ok {
			return fmt.Errorf("invalid extra-var %q (expected key=value)", ev)
		}
		extraVars[k] = v
	}

	// Determine the playbook ref: explicit arg, or discover ./site.yaml.
	playbookRef, err := resolvePlaybookRef(cmd, args)
	if err != nil {
		return err
	}

	// Resolve playbook source
	src, err := source.Resolve(playbookRef)
	if err != nil {
		return fmt.Errorf("invalid playbook source: %w", err)
	}

	ctx, cancel := signalContext(context.Background())
	defer cancel()

	playbookPath, cleanup, err := src.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch playbook: %w", err)
	}
	defer cleanup()

	// Parse playbook
	var pb *playbook.Playbook
	if info, statErr := os.Stat(playbookPath); statErr == nil && info.IsDir() {
		absRole, err := filepath.Abs(playbookPath)
		if err != nil {
			return fmt.Errorf("failed to resolve role path: %w", err)
		}
		pb = &playbook.Playbook{
			Path: absRole,
			Plays: []*playbook.Play{{
				Name:       fmt.Sprintf("Run role: %s", filepath.Base(absRole)),
				Hosts:      []string{"localhost"},
				Connection: "local",
				Roles:      []playbook.RoleRef{{Name: absRole}},
				Vars:       make(map[string]any),
			}},
		}
	} else {
		pb, err = playbook.ParseFileRaw(playbookPath)
		if err != nil {
			return fmt.Errorf("failed to parse playbook: %w", err)
		}
	}

	// Determine hosts
	hosts, err := resolveExportHosts(pb, host, allHosts)
	if err != nil {
		return err
	}

	playbookDir := filepath.Dir(playbookPath)
	rolesDir := filepath.Join(playbookDir, "roles")
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	opts := export.Options{
		Host:              host,
		AllHosts:          allHosts,
		Output:            output,
		NoFacts:           noFacts,
		CheckOnly:         checkOnly,
		NoBannerTimestamp: noBannerTimestamp,
		Tags:              tags,
		SkipTags:          skipTags,
		ExtraVars:         extraVars,
		Version:           version,
		PlaybookPath:      args[0],
		Timestamp:         timestamp,
	}

	// Compile each play for each host
	var allResults []*export.CompileResult
	hasUnsupported := false

	for _, play := range pb.Plays {
		// Load roles
		roles, cleanup, err := playbook.LoadRoles(ctx, play.Roles, rolesDir)
		if err != nil {
			return fmt.Errorf("loading roles: %w", err)
		}
		// Role files (copy/template src) may be read while compiling below;
		// defer rather than calling immediately after LoadRoles returns.
		defer cleanup()

		compiler := &export.Compiler{
			Playbook:    pb,
			Opts:        opts,
			Roles:       roles,
			PlaybookDir: playbookDir,
		}

		for _, h := range hosts {
			// Create connector for fact gathering
			var conn interface{ Connect(context.Context) error } = nil
			if !noFacts && play.ShouldGatherFacts() {
				conn := local.New()
				_ = conn
			}

			result, err := compiler.Compile(ctx, play, h, local.New())
			if err != nil {
				return fmt.Errorf("compiling for host %s: %w", h, err)
			}
			allResults = append(allResults, result)
			if len(result.Unsupported) > 0 {
				hasUnsupported = true
			}
			_ = conn
		}
	}

	// Check-only mode: print summary and exit
	if checkOnly {
		return printCheckSummary(allResults, hasUnsupported)
	}

	// Write output
	for _, result := range allResults {
		if err := writeExportResult(result, opts); err != nil {
			return err
		}
	}

	return nil
}

func resolveExportHosts(pb *playbook.Playbook, host string, allHosts bool) ([]string, error) {
	if host != "" {
		return []string{host}, nil
	}

	// Collect all hosts from plays
	var hosts []string
	seen := make(map[string]bool)
	for _, play := range pb.Plays {
		for _, h := range play.Hosts {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}

	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts found in playbook")
	}

	if !allHosts {
		if len(hosts) == 1 {
			return hosts, nil
		}
		return nil, fmt.Errorf("multiple hosts in playbook; use --host <name> or --all-hosts")
	}

	return hosts, nil
}

func writeExportResult(result *export.CompileResult, opts export.Options) error {
	if opts.Output == "" {
		// Stdout
		_, err := fmt.Print(result.Script)
		return err
	}

	if opts.AllHosts {
		// Write to directory
		dir := opts.Output
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating output directory: %w", err)
		}

		filename := sanitizeHostname(result.Host) + ".sh"
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, []byte(result.Script), 0600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "Wrote %s\n", path)

		// Update INDEX.txt
		indexPath := filepath.Join(dir, "INDEX.txt")
		f, err := os.OpenFile(indexPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("writing INDEX.txt: %w", err)
		}
		defer f.Close()
		fmt.Fprintf(f, "%s\n", filename)
	} else {
		// Write to file
		if err := os.WriteFile(opts.Output, []byte(result.Script), 0600); err != nil {
			return fmt.Errorf("writing %s: %w", opts.Output, err)
		}
		fmt.Fprintf(os.Stderr, "Wrote %s\n", opts.Output)
	}

	return nil
}

func printCheckSummary(results []*export.CompileResult, hasUnsupported bool) error {
	for _, r := range results {
		fmt.Printf("Host: %s\n", r.Host)
		fmt.Printf("  Supported tasks:   %d\n", len(r.Supported))
		fmt.Printf("  Unsupported tasks: %d\n", len(r.Unsupported))

		if len(r.Unsupported) > 0 {
			fmt.Println("  Unsupported:")
			for _, u := range r.Unsupported {
				fmt.Printf("    - %s (%s): %s\n", u.Name, u.Module, u.Reason)
			}
		}

		if len(r.Warnings) > 0 {
			fmt.Println("  Warnings:")
			for _, w := range r.Warnings {
				fmt.Printf("    - %s\n", w)
			}
		}
		fmt.Println()
	}

	if hasUnsupported {
		return fmt.Errorf("unsupported constructs found")
	}
	return nil
}

func sanitizeHostname(host string) string {
	var sb strings.Builder
	for _, c := range host {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}
