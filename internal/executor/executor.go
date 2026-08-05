// Package executor runs playbooks against target hosts.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/connector/docker"
	"github.com/tackhq/tack/internal/connector/local"
	sshconn "github.com/tackhq/tack/internal/connector/ssh"
	ssmconn "github.com/tackhq/tack/internal/connector/ssm"
	"github.com/tackhq/tack/internal/inventory"
	"github.com/tackhq/tack/internal/module"
	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
	"github.com/tackhq/tack/internal/source"
	"github.com/tackhq/tack/pkg/facts"
	"github.com/tackhq/tack/pkg/ssmparams"
)

// ConnOverrides holds CLI/env overrides for connection settings.
type ConnOverrides struct {
	Connection   string
	Hosts        []string
	SSHUser      string
	SSHPort      int
	SSHKey       string
	SSHPass      string
	HasSSHPass   bool // true when --ssh-password flag was explicitly provided
	SSHInsecure  bool
	Sudo         bool
	SudoPassword string
	SSMInstances []string
	SSMTags      map[string]string
	SSMRegion    string
	SSMBucket    string

	// SSMAttachS3Policy requests temporary IAM policy attachment for S3
	// file transfer access; true only when --ssm-attach-policy was
	// explicitly passed on the CLI.
	SSMAttachS3Policy bool

	// ConnectionInferred is true when Connection was inferred from flags
	// (e.g. --hosts with non-local targets implies ssh) rather than explicitly
	// set by the user. Inferred connections can be overridden by inventory groups.
	ConnectionInferred bool
}

// Executor runs playbooks.
type Executor struct {
	// Output handles formatted output.
	Output output.Emitter

	// DryRun only shows what would be done without making changes.
	DryRun bool

	// AutoApprove skips the interactive approval prompt.
	AutoApprove bool

	// Debug enables detailed output.
	Debug bool

	// Verbose enables full diffs in plan output.
	Verbose bool

	// ShowDiff enables file content diffs in plan output.
	ShowDiff bool

	// SkipFacts forces fact gathering off for every play, regardless of the
	// play's gather_facts setting. Set from the --no-facts CLI flag to speed
	// up runs that don't depend on system facts.
	SkipFacts bool

	// SkipPlan skips the plan preview and approval prompt, applying directly.
	// Ignored in dry-run/check mode. Set from the --no-plan CLI flag.
	SkipPlan bool

	// Forks is the number of hosts to execute concurrently.
	// Values <= 1 mean serial execution (default).
	Forks int

	// Overrides holds CLI/env connection overrides applied to each play.
	Overrides *ConnOverrides

	// PromptSudoPassword is called to prompt the user for a sudo password
	// when sudo was explicitly requested via -s/--sudo but no password was
	// provided.
	PromptSudoPassword func() (string, error)

	// SudoPromptRequested gates the upfront sudo-password prompt. It is set
	// from the -s/--sudo CLI flag (or --sudo=true in a playbook run
	// invoked with the flag) — playbook- or task-level `sudo: true` alone
	// does NOT trigger an interactive prompt. Passwordless sudo (NOPASSWD)
	// works transparently either way; if a target actually requires a
	// password, sudo itself surfaces the error, or the caller can supply
	// --sudo-password / TACK_SUDO_PASSWORD explicitly.
	SudoPromptRequested bool

	// SudoNoPrompt suppresses the upfront sudo-password prompt even when
	// SudoPromptRequested is set. Intended for CI/non-interactive runs and
	// for users who've configured passwordless sudo.
	SudoNoPrompt bool

	// ResolveVaultPassword is called to obtain the vault password when
	// a play references a vault_file and no password has been cached yet.
	ResolveVaultPassword func() ([]byte, error)

	// vaultPassword caches the resolved vault password for the run duration.
	// Zeroed in Run() deferred cleanup.
	vaultPassword []byte

	// vaultVarCache caches decrypted vault vars by resolved file path.
	// Avoids re-running Argon2id (~600ms) when multiple plays reference
	// the same vault file. Guarded by vaultMu for safe access from
	// per-host goroutines during the multi-host discovery+plan pre-pass.
	vaultVarCache map[string]map[string]any

	// vaultMu protects vaultPassword and vaultVarCache when the multi-host
	// orchestration runs vault loads from per-host goroutines.
	vaultMu sync.Mutex

	// Tags filters execution to only run tasks matching these tags.
	Tags []string

	// SkipTags filters execution to skip tasks matching these tags.
	SkipTags []string

	// Roles, when non-empty, restricts execution to tasks loaded from the named
	// roles. Play-level (non-role) tasks are skipped while this filter is active.
	Roles []string

	// Inventory holds the loaded inventory (optional). When set, group names
	// in play.Hosts are expanded and per-host vars/SSH config are applied.
	Inventory *inventory.Inventory

	// connectorFactory is a test hook overriding GetConnector. nil in
	// production; set by tests to inject fake connectors for the discovery
	// pre-pass. Must not be exported.
	connectorFactory func(play *playbook.Play, host string) (connector.Connector, error)
}

// New creates a new executor.
func New() *Executor {
	return &Executor{
		Output:        output.New(os.Stdout),
		vaultVarCache: make(map[string]map[string]any),
	}
}

// shouldGatherFacts reports whether facts should be gathered for the play,
// honoring both the play's gather_facts setting and the executor-wide
// SkipFacts override (--no-facts).
func (e *Executor) shouldGatherFacts(play *playbook.Play) bool {
	return !e.SkipFacts && play.ShouldGatherFacts()
}

// skipPlanPhase reports whether the plan preview + approval should be skipped
// in favor of going straight to apply. Never skips in dry-run/check mode.
func (e *Executor) skipPlanPhase() bool {
	return e.SkipPlan && !e.DryRun
}

// RunResult holds the result of a playbook run.
type RunResult struct {
	// Success is true if all plays completed successfully.
	Success bool

	// Stats holds execution statistics.
	Stats *Stats
}

// Stats holds execution statistics.
type Stats struct {
	Plays     int
	Tasks     int
	OK        int
	Changed   int
	Failed    int
	Skipped   int
	StartTime time.Time
	EndTime   time.Time
}

// Duration returns the total execution time.
func (s *Stats) Duration() time.Duration {
	return s.EndTime.Sub(s.StartTime)
}

// RecordResult increments the appropriate counter based on task status.
func (s *Stats) RecordResult(status string) {
	switch status {
	case "ok":
		s.OK++
	case "changed":
		s.Changed++
	case "skipped":
		s.Skipped++
	}
}

// GetOK returns the OK count (implements output.Stats).
func (s *Stats) GetOK() int { return s.OK }

// GetChanged returns the Changed count (implements output.Stats).
func (s *Stats) GetChanged() int { return s.Changed }

// GetFailed returns the Failed count (implements output.Stats).
func (s *Stats) GetFailed() int { return s.Failed }

// GetSkipped returns the Skipped count (implements output.Stats).
func (s *Stats) GetSkipped() int { return s.Skipped }

// GetDuration returns the duration (implements output.Stats).
func (s *Stats) GetDuration() time.Duration { return s.Duration() }

// PlayContext holds state for a play execution.
type PlayContext struct {
	// Play is the current play.
	Play *playbook.Play

	// Host identifies the target host this context belongs to. Empty for
	// local-connection plays where the host is implicit ("localhost"). Used
	// to tag PlannedTask entries during the plan phase so multi-host plans
	// can be aggregated and rendered with per-line attribution.
	Host string

	// Vars holds all variables (play vars + facts + registered).
	Vars map[string]any

	// Facts holds gathered system facts.
	Facts map[string]any

	// Registered holds task results stored via register.
	Registered map[string]any

	// NotifiedHandlers tracks which handlers should run.
	NotifiedHandlers map[string]bool

	// Connector is the connection to the target.
	Connector connector.Connector

	// SSMParams is a lazy-init cached SSM Parameter Store client.
	SSMParams *ssmparams.Client

	// Output is the emitter for this play context. In parallel mode,
	// each host gets its own buffered emitter.
	Output output.Emitter

	// PlaybookDir is the directory of the playbook file, used for
	// resolving relative include paths.
	PlaybookDir string
}

// Run executes a playbook.
func (e *Executor) Run(ctx context.Context, pb *playbook.Playbook) (*RunResult, error) {
	stats := &Stats{
		StartTime: time.Now(),
		Plays:     len(pb.Plays),
	}

	result := &RunResult{
		Success: true,
		Stats:   stats,
	}

	// Zero vault password and var cache at end of run (D-12).
	defer func() {
		e.vaultMu.Lock()
		defer e.vaultMu.Unlock()
		for i := range e.vaultPassword {
			e.vaultPassword[i] = 0
		}
		e.vaultPassword = nil
		for k := range e.vaultVarCache {
			delete(e.vaultVarCache, k)
		}
	}()

	e.Output.PlaybookStart(pb.Path)

	// Determine roles directory and playbook directory (relative to playbook)
	playbookDir := filepath.Dir(pb.Path)
	rolesDir := filepath.Join(playbookDir, "roles")

	for _, play := range pb.Plays {
		e.ApplyOverrides(play)
		if err := e.runPlay(ctx, play, stats, rolesDir, playbookDir); err != nil {
			if ctx.Err() != nil {
				return result, nil
			}
			result.Success = false
			e.Output.Error("Play failed: %v", err)
			break
		}
	}

	stats.EndTime = time.Now()
	e.Output.PlaybookEnd(stats)

	return result, nil
}

// ApplyOverrides applies CLI/env connection overrides to a play.
func (e *Executor) ApplyOverrides(play *playbook.Play) {
	if e.Overrides == nil {
		return
	}
	o := e.Overrides

	if o.Connection != "" {
		play.Connection = o.Connection
	}
	if len(o.Hosts) > 0 {
		play.Hosts = o.Hosts
	}
	if o.Sudo {
		play.Sudo = true
	}
	if o.SudoPassword != "" {
		play.SudoPassword = o.SudoPassword
	}

	// SSH overrides
	if o.SSHUser != "" || o.SSHPort != 0 || o.SSHKey != "" || o.HasSSHPass || o.SSHInsecure {
		if play.SSH == nil {
			play.SSH = &playbook.SSHConfig{}
		}
		if o.SSHUser != "" {
			play.SSH.User = o.SSHUser
		}
		if o.SSHPort != 0 {
			play.SSH.Port = o.SSHPort
		}
		if o.SSHKey != "" {
			play.SSH.Key = o.SSHKey
		}
		if o.HasSSHPass {
			play.SSH.Password = o.SSHPass
		}
		if o.SSHInsecure {
			f := false
			play.SSH.HostKeyChecking = &f
		}
	}

	// SSM overrides
	if o.SSMRegion != "" || o.SSMBucket != "" || len(o.SSMInstances) > 0 || len(o.SSMTags) > 0 || o.SSMAttachS3Policy {
		if play.SSM == nil {
			play.SSM = &playbook.SSMConfig{}
		}
		if o.SSMRegion != "" {
			play.SSM.Region = o.SSMRegion
		}
		if o.SSMBucket != "" {
			play.SSM.Bucket = o.SSMBucket
		}
		if o.SSMAttachS3Policy {
			play.SSM.AttachS3Policy = true
		}
		if play.Connection == "ssm" && len(play.Hosts) == 0 {
			if len(o.SSMInstances) > 0 {
				play.Hosts = o.SSMInstances
			} else if len(o.SSMTags) > 0 {
				play.SSM.Tags = o.SSMTags
			}
		}
	}
}

// runPlay executes a single play.
func (e *Executor) runPlay(ctx context.Context, play *playbook.Play, stats *Stats, rolesDir string, playbookDir string) error {
	// Handle --hosts all: expand entire inventory.
	for _, h := range play.Hosts {
		if h == "all" {
			if e.Inventory == nil {
				return fmt.Errorf("--hosts all requires an inventory file (-i flag)")
			}
			play.Hosts = e.Inventory.AllHosts()
			break
		}
	}

	// Expand inventory group names in play.Hosts and apply group-level config.
	if e.Inventory != nil && len(play.Hosts) > 0 {
		expanded := make([]string, 0, len(play.Hosts))
		for _, h := range play.Hosts {
			hosts, group, ok := e.Inventory.ExpandGroup(h)
			if !ok {
				// Not in inventory — pass through as-is (plain hostname, URI, etc.)
				expanded = append(expanded, h)
				continue
			}
			expanded = append(expanded, hosts...)
			// Apply group-level connection/SSH/SSM as defaults.
			// Group connection overrides inferred connections (e.g. --hosts infers ssh)
			// but not explicitly set ones (from playbook or -c flag).
			if group != nil {
				if group.Connection != "" && (play.Connection == "" || (e.Overrides != nil && e.Overrides.ConnectionInferred)) {
					play.Connection = group.Connection
				}
				if play.SSH == nil && group.SSH != nil {
					play.SSH = group.SSH
				}
				if play.SSM == nil && group.SSM != nil {
					play.SSM = &playbook.SSMConfig{
						Region:         group.SSM.Region,
						Bucket:         group.SSM.Bucket,
						Tags:           group.SSM.Tags,
						AttachS3Policy: group.SSM.AttachS3Policy,
					}
				}
			}
		}
		play.Hosts = expanded
	}

	if play.GetConnection() == "ssm" && play.SSM != nil && len(play.Hosts) == 0 {
		// ssm.instances is a convenience alias for hosts when connection is ssm
		if len(play.SSM.Instances) > 0 {
			play.Hosts = play.SSM.Instances
		} else if len(play.SSM.Tags) > 0 {
			// SSM tag resolution: discover instance IDs at runtime
			ids, err := ssmconn.ResolveInstancesByTags(ctx, play.SSM.Tags, play.SSM.Region)
			if err != nil {
				return fmt.Errorf("failed to resolve SSM instances by tags: %w", err)
			}
			if len(ids) == 0 {
				return fmt.Errorf("SSM tag resolution matched zero instances for tags: %v", play.SSM.Tags)
			}
			play.Hosts = ids
		}
	}

	// Validate hosts after overrides have been applied (non-local connections need hosts)
	if play.GetConnection() != "local" && len(play.Hosts) == 0 {
		return fmt.Errorf("play has no target hosts (provide via --hosts, playbook hosts: field, or -c flag)")
	}

	// Load roles if specified
	var roles []*playbook.Role
	if len(play.Roles) > 0 {
		var err error
		roles, err = playbook.LoadRoles(play.Roles, rolesDir)
		if err != nil {
			return fmt.Errorf("failed to load roles: %w", err)
		}
	}

	// Prompt for sudo password before any per-host output
	if err := e.needsSudoPassword(play); err != nil {
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	e.Output.PlayStart(play)

	// Local connection: single-host fast path.
	if play.GetConnection() == "local" {
		return e.runPlayOnHost(ctx, play, stats, roles, "localhost", playbookDir, e.Output, nil)
	}

	// Single-host non-local: keep the existing per-host orchestration so
	// the output is byte-identical to today.
	if len(play.Hosts) == 1 {
		return e.runPlayOnHost(ctx, play, stats, roles, play.Hosts[0], playbookDir, e.Output, nil)
	}

	// Multi-host: run the consolidated discover+plan pre-pass, render once,
	// prompt once, then dispatch apply.
	return e.runMultiHostPlay(ctx, play, stats, roles, playbookDir)
}

// runMultiHostPlay runs the consolidated multi-host orchestration: discover
// + plan in parallel, render one consolidated plan with per-line host
// attribution, prompt once globally, then apply (serial or via WorkerPool).
func (e *Executor) runMultiHostPlay(ctx context.Context, play *playbook.Play, stats *Stats, roles []*playbook.Role, playbookDir string) error {
	preps := e.discoverAndPlanParallel(ctx, play, roles, playbookDir)
	if preps == nil {
		// Defensive: discoverAndPlanParallel only returns nil for cases the
		// caller already filtered out (single-host, local). Should not happen.
		return fmt.Errorf("multi-host orchestration: discover+plan returned no preps")
	}

	// Multi-host summary banner — emitted once on the main thread before
	// the per-host buffers flush.
	e.Output.PlayHosts(play.Hosts)

	// Flush per-host pre-pass output (HostStart + fact-gathering result)
	// in host order. Plan output is rendered separately on the main thread
	// below.
	flushPrepBuffers(os.Stdout, play.Hosts, preps)

	if ctx.Err() != nil {
		closePrepConnectors(preps)
		return ctx.Err()
	}

	forks := e.Forks

	// Pre-pass error handling. Serial mode preserves today's fail-fast
	// semantics; parallel mode isolates failures per host.
	if forks <= 1 {
		for _, host := range play.Hosts {
			if prep := preps[host]; prep != nil && prep.err != nil {
				closePrepConnectors(preps)
				return fmt.Errorf("host %s: %w", host, prep.err)
			}
		}
	}

	// Plan preview + approval. Skipped entirely with --no-plan, which falls
	// straight through to the apply phase below.
	if !e.skipPlanPhase() {
		// Aggregate plans across hosts into a single slice. Hosts whose pre-pass
		// failed contribute nothing (their failure is recorded separately).
		var allPlanned []output.PlannedTask
		for _, host := range play.Hosts {
			prep := preps[host]
			if prep == nil || prep.err != nil {
				continue
			}
			allPlanned = append(allPlanned, prep.planned...)
		}

		// Render one consolidated plan with per-line host attribution.
		e.Output.DisplayMultiHostPlan(allPlanned, play.Hosts, e.DryRun)

		// Dry run: evaluate per-host asserts (preserves fail-fast for assert
		// preconditions), then return.
		if e.DryRun {
			for _, host := range play.Hosts {
				prep := preps[host]
				if prep == nil || prep.err != nil || prep.pctx == nil {
					continue
				}
				if err := e.evaluateAssertsForDryRun(prep.pctx, prep.allTasks); err != nil {
					closePrepConnectors(preps)
					return err
				}
			}
			closePrepConnectors(preps)
			return nil
		}

		// No drift detected — nothing to apply. Tally stats and exit.
		if allNoChange(allPlanned) {
			for _, t := range allPlanned {
				stats.Tasks++
				if t.Status == "will_skip" {
					stats.Skipped++
				} else {
					stats.OK++
				}
			}
			closePrepConnectors(preps)
			return nil
		}

		// Single global approval prompt — runs on the main thread, never inside
		// a per-host goroutine. Closes the latent stdin race in --forks > 1
		// mode that existed before this change.
		if !e.AutoApprove {
			if !e.Output.PromptApproval(formatApprovalTarget(play.Hosts, play.GetConnection())) {
				e.Output.Info("Apply cancelled.")
				closePrepConnectors(preps)
				return nil
			}
		}
	}

	// --- Apply phase ---
	hosts := play.Hosts

	if forks <= 1 {
		// Serial apply.
		for _, host := range hosts {
			prep := preps[host]
			if prep == nil || prep.err != nil || prep.pctx == nil {
				continue
			}
			if err := e.applyHostPlan(ctx, prep.pctx, stats, prep.allTasks, prep.allHandlers); err != nil {
				_ = prep.conn.Close()
				prep.conn = nil
				// Close remaining hosts' connections that we won't use.
				closePrepConnectors(preps)
				return err
			}
			_ = prep.conn.Close()
			prep.conn = nil
		}
		return nil
	}

	// Parallel apply. Each goroutine runs apply for one host; output is
	// buffered and flushed in host order after the pool drains.
	pool := NewWorkerPool(forks)
	for _, host := range hosts {
		host := host
		prep := preps[host]

		// Pre-pass already failed for this host — record as a failure with
		// no apply attempt.
		if prep == nil || prep.err != nil {
			var perr error
			if prep != nil {
				perr = prep.err
			}
			pool.Submit(ctx, func(ctx context.Context) *HostResult {
				return &HostResult{
					Host:    host,
					Success: false,
					Error:   perr,
					Output:  &bytes.Buffer{},
				}
			})
			continue
		}

		pool.Submit(ctx, func(ctx context.Context) *HostResult {
			buf := &bytes.Buffer{}
			hostOutput := output.New(buf)
			if textOut, ok := e.Output.(*output.Output); ok {
				hostOutput.SetColor(textOut.ColorEnabled())
			}
			hostOutput.SetDebug(e.Debug)
			hostOutput.SetVerbose(e.Verbose)
			hostOutput.SetDiff(e.ShowDiff)

			// Re-target the prepared pctx's emitter to the per-host buffer
			// for the apply phase. The pre-pass emitter (which was buffered
			// and already flushed) is no longer relevant.
			prep.pctx.Output = hostOutput
			hostOutput.HostStart(host, play.GetConnection())

			hostStats := &Stats{}
			err := e.applyHostPlan(ctx, prep.pctx, hostStats, prep.allTasks, prep.allHandlers)
			_ = prep.conn.Close()
			prep.conn = nil

			return &HostResult{
				Host:    host,
				Success: err == nil,
				Error:   err,
				Stats:   *hostStats,
				Output:  buf,
			}
		})
	}

	results := pool.Wait()

	// Flush buffered output in host order
	FlushBuffers(os.Stdout, hosts, results)

	// Aggregate stats and errors
	var failed []string
	for _, r := range results {
		stats.Tasks += r.Stats.Tasks
		stats.OK += r.Stats.OK
		stats.Changed += r.Stats.Changed
		stats.Failed += r.Stats.Failed
		stats.Skipped += r.Stats.Skipped
		if !r.Success {
			failed = append(failed, r.Host)
		}
	}

	if len(failed) > 0 {
		// Show per-host failure summary
		for _, r := range results {
			if !r.Success && r.Error != nil {
				e.Output.Error("Host %s failed: %v", r.Host, r.Error)
			}
		}
		return fmt.Errorf("%d host(s) failed: %v", len(failed), failed)
	}

	return nil
}

// preparePlayContext builds the per-host PlayContext: merges play/role/
// inventory/vault vars, opens (or reuses) the connector, gathers facts, and
// initializes the SSM client. Caller is responsible for closing pctx.Connector.
//
// When prep != nil, the discovery pre-pass already opened the connector and
// gathered facts; preparePlayContext reuses them and emits no "Gathering
// Facts" line (the pre-pass already did so into its own buffer). When prep
// is nil, this method runs the inline Connect + facts.Gather path used by
// single-host plays and the local connection.
func (e *Executor) preparePlayContext(ctx context.Context, play *playbook.Play, roles []*playbook.Role, host string, playbookDir string, emitter output.Emitter, prep *hostPrep) (*PlayContext, error) {
	pctx := &PlayContext{
		Play:             play,
		Host:             host,
		Vars:             make(map[string]any),
		Facts:            make(map[string]any),
		Registered:       make(map[string]any),
		NotifiedHandlers: make(map[string]bool),
		Output:           emitter,
		PlaybookDir:      playbookDir,
	}

	// Merge variables with correct precedence: role defaults < role vars < play vars
	pctx.Vars = playbook.MergeRoleVars(roles, play.Vars)

	// Merge vars_files (higher priority than play vars, lower than inventory vars)
	if len(play.VarsFiles) > 0 {
		vfVars, err := e.loadVarsFiles(play, playbookDir, pctx.Vars)
		if err != nil {
			return nil, fmt.Errorf("vars_files: %w", err)
		}
		for k, v := range vfVars {
			pctx.Vars[k] = v
		}
	}

	// Inject inventory vars as lower-priority defaults (play vars take precedence).
	// Group vars are lowest, per-host vars are higher (but still below play vars).
	if e.Inventory != nil {
		for _, g := range e.Inventory.GetHostGroups(host) {
			for k, v := range g.Vars {
				if _, exists := pctx.Vars[k]; !exists {
					pctx.Vars[k] = v
				}
			}
		}
		if entry := e.Inventory.GetHost(host); entry != nil {
			for k, v := range entry.Vars {
				if _, exists := pctx.Vars[k]; !exists {
					pctx.Vars[k] = v
				}
			}
		}
	}

	// Merge vault variables (between play vars and facts in precedence)
	if play.VaultFile != "" {
		vaultVars, err := e.loadVaultVars(play, playbookDir)
		if err != nil {
			return nil, fmt.Errorf("vault: %w", err)
		}
		for k, v := range vaultVars {
			if _, exists := pctx.Vars[k]; !exists {
				pctx.Vars[k] = v
			}
		}
	}

	// Add environment variables
	pctx.Vars["env"] = getEnvMap()

	// Get connector and gather facts. When the discovery pre-pass already
	// did this work for us, reuse its connector + facts and skip the inline
	// path entirely (the pre-pass also already emitted the "Gathering Facts"
	// task line for this host).
	if prep != nil {
		pctx.Connector = prep.conn
		if prep.facts != nil {
			pctx.Facts = prep.facts
			pctx.Vars["facts"] = prep.facts
		}
	} else {
		conn, err := e.GetConnector(play, host)
		if err != nil {
			// HostStart left the banner line open; close it via the
			// fact-result API so the error renders on its own line.
			emitter.HostFactsResult(host, false, err.Error())
			return nil, fmt.Errorf("failed to create connector for host %s: %w", host, err)
		}
		pctx.Connector = conn

		if err := conn.Connect(ctx); err != nil {
			emitter.HostFactsResult(host, false, err.Error())
			_ = conn.Close()
			return nil, fmt.Errorf("failed to connect to %s: %w", host, err)
		}

		if e.shouldGatherFacts(play) {
			f, err := facts.Gather(ctx, conn)
			if err != nil {
				emitter.HostFactsResult(host, false, err.Error())
				_ = conn.Close()
				return nil, fmt.Errorf("failed to gather facts: %w", err)
			}
			pctx.Facts = f
			pctx.Vars["facts"] = f
			emitter.HostFactsResult(host, true, "")
		} else {
			emitter.HostStartDone(host)
		}
	}

	// Create lazy SSM Parameter Store client.
	// Region priority: play.SSM.Region > ec2_region fact > AWS SDK default.
	ssmRegion := ""
	if play.SSM != nil && play.SSM.Region != "" {
		ssmRegion = play.SSM.Region
	} else if r, ok := pctx.Facts["ec2_region"].(string); ok && r != "" {
		ssmRegion = r
	}
	pctx.SSMParams = ssmparams.New(ssmRegion)

	return pctx, nil
}

// computeHostPlan produces a host's full plan slice (tasks + handlers).
func (e *Executor) computeHostPlan(ctx context.Context, pctx *PlayContext, allTasks, allHandlers []*playbook.Task) []output.PlannedTask {
	planned := e.planTasks(ctx, pctx, allTasks, pctx.Output)
	if len(allHandlers) > 0 {
		planned = append(planned, e.planHandlers(pctx.Host, allTasks, planned, allHandlers)...)
	}
	return planned
}

// runPlayOnHost executes a play against a single host. Used for single-host
// plays and the local-connection path; it owns rendering the plan, prompting
// for approval, and dispatching the apply phase.
//
// The emitter parameter controls where output is written. When prep is
// non-nil the discovery pre-pass already opened the connector and gathered
// facts; runPlayOnHost reuses them.
func (e *Executor) runPlayOnHost(ctx context.Context, play *playbook.Play, stats *Stats, roles []*playbook.Role, host string, playbookDir string, emitter output.Emitter, prep *hostPrep) error {
	emitter.HostStart(host, play.GetConnection())

	pctx, err := e.preparePlayContext(ctx, play, roles, host, playbookDir, emitter, prep)
	if err != nil {
		return err
	}
	defer pctx.Connector.Close()

	// Expand role tasks and handlers
	allTasks := playbook.ExpandRoleTasks(roles, play.Tasks)
	allHandlers := playbook.ExpandRoleHandlers(roles, play.Handlers)

	// Skip the plan preview + approval entirely and go straight to apply.
	if e.skipPlanPhase() {
		return e.applyHostPlan(ctx, pctx, stats, allTasks, allHandlers)
	}

	// --- Plan phase ---
	planned := e.computeHostPlan(ctx, pctx, allTasks, allHandlers)
	emitter.DisplayPlan(planned, e.DryRun)

	// Dry run stops after showing the plan, but assert failures still fail
	// the play so preconditions fail-fast regardless of mode.
	if e.DryRun {
		if err := e.evaluateAssertsForDryRun(pctx, allTasks); err != nil {
			return err
		}
		return nil
	}

	// No drift detected — nothing to apply
	if allNoChange(planned) {
		for _, t := range planned {
			stats.Tasks++
			if t.Status == "will_skip" {
				stats.Skipped++
			} else {
				stats.OK++
			}
		}
		return nil
	}

	// Prompt for approval unless auto-approved
	if !e.AutoApprove {
		if !emitter.PromptApproval(formatApprovalTarget([]string{host}, play.GetConnection())) {
			emitter.Info("Apply cancelled.")
			return nil
		}
	}

	return e.applyHostPlan(ctx, pctx, stats, allTasks, allHandlers)
}

// applyHostPlan runs the apply phase on a prepared PlayContext. The pctx
// must already have its connector connected and facts gathered. The caller
// owns lifecycle of pctx.Connector (apply does not Close it).
func (e *Executor) applyHostPlan(ctx context.Context, pctx *PlayContext, stats *Stats, allTasks, allHandlers []*playbook.Task) error {
	emitter := pctx.Output
	play := pctx.Play
	playTags := play.Tags
	for _, task := range allTasks {
		// Role filtering: when --roles is active, only run tasks from the named
		// roles. Applied at the top level so a filtered-out block is skipped as
		// a unit (block sub-tasks do not carry a role name).
		if !shouldRunRole(task.RoleName, e.Roles) {
			emitter.TaskResult(task.String(), "skipped", false, "skipped (role)", effectiveTags(task, playTags, nil))
			stats.Tasks++
			stats.Skipped++
			continue
		}

		// Handle block directive
		if task.IsBlock() {
			if err := e.runBlock(ctx, pctx, task, stats, nil, playTags, nil); err != nil {
				if !task.IgnoreErrors {
					return err
				}
				emitter.TaskResult(task.String(), "failed (ignored)", false, err.Error(), effectiveTags(task, playTags, nil))
			}
			continue
		}

		// Tag filtering (effective tags also drive inline display)
		eTags := effectiveTags(task, playTags, nil)

		// Handle include directive
		if task.Include != "" {
			if !shouldRunTask(eTags, e.Tags, e.SkipTags) {
				emitter.TaskResult(task.String(), "skipped", false, "skipped (tag)", eTags)
				stats.Tasks++
				stats.Skipped++
				continue
			}
			if err := e.runInclude(ctx, pctx, task, stats, nil, playTags, nil); err != nil {
				if !task.IgnoreErrors {
					return err
				}
				emitter.TaskResult(task.String(), "failed (ignored)", false, err.Error(), eTags)
			}
			continue
		}

		if !shouldRunTask(eTags, e.Tags, e.SkipTags) {
			emitter.TaskResult(task.String(), "skipped", false, "skipped (tag)", eTags)
			stats.Tasks++
			stats.Skipped++
			continue
		}

		stats.Tasks++

		taskResult, err := e.runTask(ctx, pctx, task, eTags)
		if err != nil {
			stats.Failed++
			if !task.IgnoreErrors {
				return err
			}
			emitter.TaskResult(task.String(), "failed (ignored)", false, err.Error(), eTags)
			continue
		}

		stats.RecordResult(taskResult.Status)
	}

	// Run notified handlers (using expanded handlers)
	if err := e.runHandlersExpanded(ctx, pctx, stats, allHandlers); err != nil {
		return err
	}

	return nil
}

// TaskResult holds the result of a task execution.
type TaskResult struct {
	Status  string // ok, changed, skipped, failed
	Changed bool
	Data    map[string]any
	Error   error
}

// runTask executes a single task. eTags is the task's effective tag set, shown
// inline so users can see what -t/--skip-tags would match.
func (e *Executor) runTask(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	taskName := task.String()

	// Check 'when' condition
	if task.When != "" {
		shouldRun, err := e.evaluateCondition(task.When, pctx)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate 'when' condition: %w", err)
		}
		if !shouldRun {
			pctx.Output.TaskResult(taskName, "skipped", false, "when condition not met", eTags)
			return &TaskResult{Status: "skipped"}, nil
		}
	}

	// Assert built-in task keyword: evaluate locally, bypass connector/module.
	if task.IsAssert() {
		return e.executeAssert(ctx, pctx, task, eTags)
	}

	// Resolve loop expression (e.g. "{{ windmill_files }}") to a concrete list
	if task.LoopExpr != "" && len(task.Loop) == 0 {
		resolved, err := e.interpolateString(ctx, task.LoopExpr, pctx)
		if err == nil {
			if items, ok := resolved.([]any); ok {
				task.Loop = items
			}
		}
	}

	// Handle loops
	if len(task.Loop) > 0 {
		return e.runTaskLoop(ctx, pctx, task, eTags)
	}

	// Run single task
	return e.runSingleTask(ctx, pctx, task, eTags)
}

// runSingleTask executes a task once. eTags is the task's effective tag set,
// forwarded to the result line for inline display.
func (e *Executor) runSingleTask(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	taskName := task.String()
	pctx.Output.TaskStart(taskName, task.Module)

	// Handle task-level sudo override
	playSudo := pctx.Play.Sudo
	taskSudo := task.ShouldSudo(playSudo)
	if taskSudo != playSudo {
		pctx.Connector.SetSudo(taskSudo, pctx.Play.SudoPassword)
		defer pctx.Connector.SetSudo(playSudo, pctx.Play.SudoPassword)
	}

	// Expand shorthand syntax
	playbook.ExpandShorthand(task)

	// Resolve module
	mod := module.Get(task.Module)
	if mod == nil {
		err := fmt.Errorf("unknown module: %s", task.Module)
		pctx.Output.TaskResult(taskName, "failed", false, err.Error(), eTags)
		return nil, err
	}

	// Interpolate variables in params
	params, err := e.interpolateParams(ctx, task.Params, pctx)
	if err != nil {
		pctx.Output.TaskResult(taskName, "failed", false, err.Error(), eTags)
		return nil, fmt.Errorf("failed to interpolate parameters: %w", err)
	}

	// Inject role path for role tasks (allows modules like copy to find role files)
	if task.RolePath != "" {
		params["_role_path"] = task.RolePath
	}

	// Inject template variables and SSM params client for template module
	if task.Module == "template" {
		params["_template_vars"] = pctx.Vars
		if pctx.SSMParams != nil {
			params["_ssm_params"] = pctx.SSMParams
		}
	}

	// Handle dry run
	if e.DryRun {
		pctx.Output.TaskResult(taskName, "skipped (dry run)", false, "", eTags)
		return &TaskResult{Status: "skipped"}, nil
	}

	// Execute with retries
	var result *module.Result
	var lastErr error
	maxAttempts := task.Retries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			pctx.Output.Info("Retry %d/%d for task: %s", attempt, maxAttempts, taskName)
			time.Sleep(time.Duration(task.Delay) * time.Second)
		}

		result, lastErr = mod.Run(ctx, pctx.Connector, params)
		if lastErr == nil {
			break
		}
	}

	if lastErr != nil {
		pctx.Output.TaskResult(taskName, "failed", false, lastErr.Error(), eTags)
		return &TaskResult{Status: "failed", Error: lastErr}, lastErr
	}

	// Store registered result. Module-specific Data keys are hoisted to the
	// top level so playbooks can write `{{ reg.field }}` as documented
	// (docs/modules/*.md, llms.txt, examples/). The nested `data` map is
	// preserved for back-compat with playbooks that use `{{ reg.data.field }}`.
	if task.Register != "" {
		reg := map[string]any{
			"changed": result.Changed,
			"message": result.Message,
			"data":    result.Data,
		}
		for k, v := range result.Data {
			if _, reserved := reg[k]; reserved {
				continue
			}
			reg[k] = v
		}
		pctx.Registered[task.Register] = reg
		pctx.Vars[task.Register] = reg
	}

	// Handle notify
	if result.Changed && len(task.Notify) > 0 {
		for _, handler := range task.Notify {
			pctx.NotifiedHandlers[handler] = true
		}
	}

	// Determine status
	status := "ok"
	if result.Changed {
		status = "changed"
	}

	pctx.Output.TaskResult(taskName, status, result.Changed, result.Message, eTags)

	return &TaskResult{
		Status:  status,
		Changed: result.Changed,
		Data:    result.Data,
	}, nil
}

// runTaskLoop executes a task for each item in a loop.
func (e *Executor) runTaskLoop(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	loopVar := task.GetLoopVar()
	var anyChanged bool

	for i, item := range task.Loop {
		// Set loop variable
		pctx.Vars[loopVar] = item
		pctx.Vars["loop_index"] = i

		result, err := e.runSingleTask(ctx, pctx, task, eTags)
		if err != nil {
			return result, err
		}

		if result.Changed {
			anyChanged = true
		}
	}

	// Clean up loop variables
	delete(pctx.Vars, loopVar)
	delete(pctx.Vars, "loop_index")

	status := "ok"
	if anyChanged {
		status = "changed"
	}

	return &TaskResult{Status: status, Changed: anyChanged}, nil
}

// runHandlersExpanded executes notified handlers from the expanded handlers list.
func (e *Executor) runHandlersExpanded(ctx context.Context, pctx *PlayContext, stats *Stats, handlers []*playbook.Task) error {
	if len(pctx.NotifiedHandlers) == 0 {
		return nil
	}

	pctx.Output.Section("RUNNING HANDLERS")

	for _, handler := range handlers {
		if !pctx.NotifiedHandlers[handler.Name] {
			continue
		}

		eTags := effectiveTags(handler, pctx.Play.Tags, nil)

		// Handlers ignore --tags but respect --skip-tags
		if len(e.SkipTags) > 0 {
			if !shouldRunTask(eTags, nil, e.SkipTags) {
				pctx.Output.TaskResult(handler.String(), "skipped", false, "skipped (tag)", eTags)
				stats.Tasks++
				stats.Skipped++
				continue
			}
		}

		stats.Tasks++

		result, err := e.runSingleTask(ctx, pctx, handler, eTags)
		if err != nil {
			stats.Failed++
			return fmt.Errorf("handler '%s' failed: %w", handler.Name, err)
		}

		stats.RecordResult(result.Status)
	}

	return nil
}

// runBlock executes a block/rescue/always task group.
func (e *Executor) runBlock(ctx context.Context, pctx *PlayContext, task *playbook.Task, stats *Stats, visitedPaths []string, playTags []string, inheritedBlockTags []string) error {
	blockName := task.String()

	// Compute block-level effective tags for the block itself
	blockETags := effectiveTags(task, playTags, inheritedBlockTags)
	if !shouldRunTask(blockETags, e.Tags, e.SkipTags) {
		pctx.Output.TaskResult(blockName, "skipped", false, "skipped (tag)", blockETags)
		stats.Tasks++
		stats.Skipped++
		return nil
	}

	// Check block-level 'when' condition — skip entire block if false
	if task.When != "" {
		shouldRun, err := e.evaluateCondition(task.When, pctx)
		if err != nil {
			return fmt.Errorf("failed to evaluate block 'when' condition: %w", err)
		}
		if !shouldRun {
			pctx.Output.TaskResult(blockName, "skipped", false, "when condition not met", blockETags)
			stats.Tasks++
			stats.Skipped++
			return nil
		}
	}

	pctx.Output.Section("BLOCK: " + blockName)

	// Merge block tags into inherited tags for child tasks
	childBlockTags := mergeBlockTags(inheritedBlockTags, task.Tags)

	// Handle block-level sudo inheritance: apply to child tasks that don't override
	var restoreSudo func()
	if task.Sudo != nil {
		blockSudo := *task.Sudo
		origPlaySudo := pctx.Play.Sudo
		pctx.Play.Sudo = blockSudo
		restoreSudo = func() {
			pctx.Play.Sudo = origPlaySudo
		}
	}

	// Execute block tasks
	var blockErr error
	for _, bt := range task.Block {
		if err := e.runBlockTask(ctx, pctx, bt, stats, visitedPaths, playTags, childBlockTags); err != nil {
			blockErr = err
			stats.Failed++
			break
		}
	}

	// Execute rescue tasks if block failed and rescue exists
	var rescueErr error
	if blockErr != nil && len(task.Rescue) > 0 {
		pctx.Output.Section("RESCUE: " + blockName)
		for _, rt := range task.Rescue {
			if err := e.runBlockTask(ctx, pctx, rt, stats, visitedPaths, playTags, childBlockTags); err != nil {
				rescueErr = err
				stats.Failed++
				break
			}
		}
	}

	// Execute always tasks regardless
	var alwaysErr error
	if len(task.Always) > 0 {
		pctx.Output.Section("ALWAYS: " + blockName)
		for _, at := range task.Always {
			if err := e.runBlockTask(ctx, pctx, at, stats, visitedPaths, playTags, childBlockTags); err != nil {
				alwaysErr = err
				stats.Failed++
				break
			}
		}
	}

	// Restore play-level sudo
	if restoreSudo != nil {
		restoreSudo()
	}

	// Determine final error:
	// - If rescue succeeded, block is recovered (no error)
	// - If rescue failed or was absent and block failed, propagate error
	// - Always errors are propagated regardless
	if alwaysErr != nil {
		return alwaysErr
	}
	if blockErr != nil {
		if len(task.Rescue) > 0 && rescueErr == nil {
			// Rescue succeeded — recovered
			return nil
		}
		if rescueErr != nil {
			return rescueErr
		}
		return blockErr
	}
	return nil
}

// mergeBlockTags combines inherited block tags with a block's own tags.
func mergeBlockTags(inherited, own []string) []string {
	if len(inherited) == 0 {
		return own
	}
	if len(own) == 0 {
		return inherited
	}
	seen := make(map[string]bool, len(inherited))
	result := make([]string, len(inherited))
	copy(result, inherited)
	for _, t := range inherited {
		seen[t] = true
	}
	for _, t := range own {
		if !seen[t] {
			result = append(result, t)
		}
	}
	return result
}

// runBlockTask executes a single task within a block/rescue/always section.
// It handles includes, nested blocks, and regular tasks.
func (e *Executor) runBlockTask(ctx context.Context, pctx *PlayContext, task *playbook.Task, stats *Stats, visitedPaths []string, playTags []string, blockTags []string) error {
	if task.IsBlock() {
		return e.runBlock(ctx, pctx, task, stats, visitedPaths, playTags, blockTags)
	}

	// Tag filtering for block child tasks
	eTags := effectiveTags(task, playTags, blockTags)
	if !shouldRunTask(eTags, e.Tags, e.SkipTags) {
		pctx.Output.TaskResult(task.String(), "skipped", false, "skipped (tag)", eTags)
		stats.Tasks++
		stats.Skipped++
		return nil
	}

	if task.Include != "" {
		if err := e.runInclude(ctx, pctx, task, stats, visitedPaths, playTags, blockTags); err != nil {
			if !task.IgnoreErrors {
				return err
			}
			pctx.Output.TaskResult(task.String(), "failed (ignored)", false, err.Error(), eTags)
		}
		return nil
	}

	stats.Tasks++
	taskResult, err := e.runTask(ctx, pctx, task, eTags)
	if err != nil {
		if !task.IgnoreErrors {
			return err
		}
		pctx.Output.TaskResult(task.String(), "failed (ignored)", false, err.Error(), eTags)
		return nil
	}
	stats.RecordResult(taskResult.Status)
	return nil
}

// maxIncludeDepth is the maximum nesting depth for include directives.
const maxIncludeDepth = 64

// runInclude handles an include/include_tasks directive during the apply phase.
// visitedPaths tracks the include chain for circular detection.
func (e *Executor) runInclude(ctx context.Context, pctx *PlayContext, task *playbook.Task, stats *Stats, visitedPaths []string, playTags, blockTags []string) error {
	taskName := task.String()

	// Check 'when' condition
	if task.When != "" {
		shouldRun, err := e.evaluateCondition(task.When, pctx)
		if err != nil {
			return fmt.Errorf("failed to evaluate 'when' condition: %w", err)
		}
		if !shouldRun {
			pctx.Output.TaskResult(taskName, "skipped", false, "when condition not met", effectiveTags(task, playTags, blockTags))
			stats.Tasks++
			stats.Skipped++
			return nil
		}
	}

	// Handle loop on include_tasks
	if len(task.Loop) > 0 || task.LoopExpr != "" {
		return e.runIncludeLoop(ctx, pctx, task, stats, visitedPaths, playTags, blockTags)
	}

	return e.runIncludeOnce(ctx, pctx, task, stats, visitedPaths, playTags, blockTags)
}

// runIncludeLoop executes an include directive once per loop iteration.
func (e *Executor) runIncludeLoop(ctx context.Context, pctx *PlayContext, task *playbook.Task, stats *Stats, visitedPaths []string, playTags, blockTags []string) error {
	taskName := task.String()

	// Resolve loop expression if needed
	loop := task.Loop
	if task.LoopExpr != "" && len(loop) == 0 {
		resolved, err := e.interpolateString(ctx, task.LoopExpr, pctx)
		if err != nil {
			return fmt.Errorf("failed to resolve loop expression: %w", err)
		}
		items, ok := resolved.([]any)
		if !ok {
			return fmt.Errorf("loop expression must resolve to a list, got %T", resolved)
		}
		loop = items
	}

	loopVar := task.GetLoopVar()

	for i, item := range loop {
		// Set loop variables
		pctx.Vars[loopVar] = item
		pctx.Vars["loop_index"] = i

		if err := e.runIncludeOnce(ctx, pctx, task, stats, visitedPaths, playTags, blockTags); err != nil {
			return err
		}
	}

	// Clean up loop variables
	delete(pctx.Vars, loopVar)
	delete(pctx.Vars, "loop_index")

	_ = taskName
	return nil
}

// runIncludeOnce executes a single include with vars scoping and circular detection.
func (e *Executor) runIncludeOnce(ctx context.Context, pctx *PlayContext, task *playbook.Task, stats *Stats, visitedPaths []string, playTags, blockTags []string) error {
	taskName := task.String()

	// Interpolate variables in the include path
	includePath, err := e.interpolateString(ctx, task.Include, pctx)
	if err != nil {
		return fmt.Errorf("failed to interpolate include path: %w", err)
	}
	includeStr, ok := includePath.(string)
	if !ok {
		return fmt.Errorf("include path must be a string, got %T", includePath)
	}

	// Resolve path: role-relative, playbook-relative, or absolute
	resolvedPath := e.resolveIncludePath(includeStr, task.RolePath, pctx.PlaybookDir)

	// Circular include detection
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		absPath = resolvedPath
	}
	// Try to resolve symlinks for accurate cycle detection
	if evaluated, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = evaluated
	}

	// Check max depth
	if len(visitedPaths) >= maxIncludeDepth {
		return fmt.Errorf("maximum include depth (%d) exceeded", maxIncludeDepth)
	}

	// Check for circular include
	for _, vp := range visitedPaths {
		if vp == absPath {
			chain := append(visitedPaths, absPath)
			return fmt.Errorf("circular include detected: %s", strings.Join(chain, " → "))
		}
	}

	newVisited := append(append([]string(nil), visitedPaths...), absPath)

	pctx.Output.TaskStart(taskName, "include_tasks")

	// Resolve and fetch the source (handles local, git, s3, http)
	src, err := source.Resolve(includeStr)
	if err != nil {
		return fmt.Errorf("failed to resolve include source %q: %w", includeStr, err)
	}

	// For local sources, use the resolved path instead
	if _, isLocal := src.(*source.LocalSource); isLocal {
		src = &source.LocalSource{Path: resolvedPath}
	}

	localPath, cleanup, err := src.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch include source %q: %w", includeStr, err)
	}
	defer cleanup()

	// Parse the included tasks file
	includedTasks, err := playbook.LoadTasksFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to parse included tasks from %q: %w", includeStr, err)
	}

	pctx.Output.TaskResult(taskName, "ok", false, fmt.Sprintf("included %d tasks from %s", len(includedTasks), includeStr), effectiveTags(task, playTags, blockTags))

	// Scope variables from IncludeVars: snapshot, merge, execute, restore
	var savedVars map[string]any
	var injectedKeys []string
	if len(task.IncludeVars) > 0 {
		savedVars = make(map[string]any)
		for k, v := range task.IncludeVars {
			if existing, exists := pctx.Vars[k]; exists {
				savedVars[k] = existing
			} else {
				injectedKeys = append(injectedKeys, k)
			}
			pctx.Vars[k] = v
		}
	}

	// Execute each included task inline. Included tasks inherit the include's
	// tag context (play + block tags) for both filtering and display.
	for _, inclTask := range includedTasks {
		inclETags := effectiveTags(inclTask, playTags, blockTags)

		// Handle nested includes
		if inclTask.Include != "" {
			if err := e.runInclude(ctx, pctx, inclTask, stats, newVisited, playTags, blockTags); err != nil {
				if !inclTask.IgnoreErrors {
					e.restoreIncludeVars(pctx, savedVars, injectedKeys)
					return err
				}
				pctx.Output.TaskResult(inclTask.String(), "failed (ignored)", false, err.Error(), inclETags)
			}
			continue
		}

		stats.Tasks++

		taskResult, err := e.runTask(ctx, pctx, inclTask, inclETags)
		if err != nil {
			stats.Failed++
			if !inclTask.IgnoreErrors {
				e.restoreIncludeVars(pctx, savedVars, injectedKeys)
				return err
			}
			pctx.Output.TaskResult(inclTask.String(), "failed (ignored)", false, err.Error(), inclETags)
			continue
		}

		stats.RecordResult(taskResult.Status)
	}

	// Restore vars scope
	e.restoreIncludeVars(pctx, savedVars, injectedKeys)

	return nil
}

// restoreIncludeVars restores variables after an include completes.
func (e *Executor) restoreIncludeVars(pctx *PlayContext, savedVars map[string]any, injectedKeys []string) {
	// Restore overridden vars
	for k, v := range savedVars {
		pctx.Vars[k] = v
	}
	// Remove vars that were injected (didn't exist before)
	for _, k := range injectedKeys {
		delete(pctx.Vars, k)
	}
}

// resolveIncludePath resolves an include path relative to the appropriate base directory.
func (e *Executor) resolveIncludePath(includePath, rolePath, playbookDir string) string {
	// Absolute paths are used as-is
	if filepath.IsAbs(includePath) {
		return includePath
	}

	// URLs and special prefixes are not resolved
	if strings.Contains(includePath, "://") || strings.HasPrefix(includePath, "git@") {
		return includePath
	}

	// Role-relative: resolve against role's tasks/ directory
	if rolePath != "" {
		return filepath.Join(rolePath, "tasks", includePath)
	}

	// Playbook-relative
	if playbookDir != "" {
		return filepath.Join(playbookDir, includePath)
	}

	return includePath
}

// planTasks evaluates tasks without executing them and returns a plan.
func (e *Executor) planTasks(ctx context.Context, pctx *PlayContext, tasks []*playbook.Task, emitter output.Emitter, blockTags ...[]string) []output.PlannedTask {
	var inheritedBlockTags []string
	if len(blockTags) > 0 {
		inheritedBlockTags = blockTags[0]
	}
	var plan []output.PlannedTask

	// Track which variable names are registered by preceding tasks,
	// so we can detect conditions that depend on runtime results.
	registeredNames := make(map[string]bool)
	for k := range pctx.Registered {
		registeredNames[k] = true
	}

	var playTags []string
	if pctx.Play != nil {
		playTags = pctx.Play.Tags
	}

	for _, task := range tasks {
		eTags := effectiveTags(task, playTags, inheritedBlockTags)

		// Role filtering in plan phase (mirrors applyHostPlan)
		if !shouldRunRole(task.RoleName, e.Roles) {
			plan = append(plan, output.PlannedTask{
				Host:   pctx.Host,
				Name:   task.String(),
				Module: task.Module,
				Status: "will_skip",
				Reason: "skipped (role)",
				Tags:   eTags,
			})
			if task.Register != "" {
				registeredNames[task.Register] = true
			}
			continue
		}

		// Tag filtering in plan phase
		if !shouldRunTask(eTags, e.Tags, e.SkipTags) {
			plan = append(plan, output.PlannedTask{
				Host:   pctx.Host,
				Name:   task.String(),
				Module: task.Module,
				Status: "will_skip",
				Reason: "skipped (tag)",
				Tags:   eTags,
			})
			if task.Register != "" {
				registeredNames[task.Register] = true
			}
			continue
		}

		// Handle include tasks in plan phase
		if task.Include != "" {
			pt := output.PlannedTask{
				Host:   pctx.Host,
				Name:   task.String(),
				Module: "include_tasks",
				Status: "will_run",
				Params: map[string]any{"source": task.Include},
				Tags:   eTags,
			}
			if task.When != "" {
				if e.conditionReferencesRegistered(task.When, registeredNames) {
					pt.Status = "conditional"
					pt.Reason = task.When
				} else {
					shouldRun, err := e.evaluateCondition(task.When, pctx)
					if err != nil || !shouldRun {
						pt.Status = "will_skip"
						pt.Reason = "when: " + task.When
					}
				}
			}
			plan = append(plan, pt)
			continue
		}

		// Handle block tasks in plan phase
		if task.IsBlock() {
			plan = append(plan, e.planBlock(ctx, pctx, task, 0, registeredNames)...)
			continue
		}

		// Handle assert built-in task in plan phase
		if task.IsAssert() {
			pt := output.PlannedTask{
				Host:   pctx.Host,
				Name:   task.String(),
				Module: "assert",
				Status: "will_run",
				Tags:   eTags,
			}
			if task.When != "" {
				if e.conditionReferencesRegistered(task.When, registeredNames) {
					pt.Status = "conditional"
					pt.Reason = task.When
				} else {
					shouldRun, err := e.evaluateCondition(task.When, pctx)
					if err != nil || !shouldRun {
						pt.Status = "will_skip"
						pt.Reason = "when: " + task.When
					}
				}
			}
			if task.Register != "" {
				registeredNames[task.Register] = true
			}
			plan = append(plan, pt)
			continue
		}

		pt := output.PlannedTask{
			Host:   pctx.Host,
			Name:   task.String(),
			Module: task.Module,
			Tags:   eTags,
		}

		// Resolve loop expression for plan display
		if task.LoopExpr != "" && len(task.Loop) == 0 {
			resolved, err := e.interpolateString(ctx, task.LoopExpr, pctx)
			if err == nil {
				if items, ok := resolved.([]any); ok {
					task.Loop = items
				}
			}
		}

		if len(task.Loop) > 0 {
			pt.LoopCount = len(task.Loop)
		}

		if task.When != "" {
			// Check if the condition references a registered variable
			if e.conditionReferencesRegistered(task.When, registeredNames) {
				pt.Status = "conditional"
				pt.Reason = task.When
			} else {
				shouldRun, err := e.evaluateCondition(task.When, pctx)
				if err != nil || !shouldRun {
					pt.Status = "will_skip"
					pt.Reason = "when: " + task.When
				} else {
					pt.Status = "will_run"
				}
			}
		} else {
			pt.Status = "will_run"
		}

		// For looped tasks, temporarily set the loop variable to the first item
		// so that param interpolation and checks produce meaningful results.
		if len(task.Loop) > 0 {
			loopVar := task.GetLoopVar()
			pctx.Vars[loopVar] = task.Loop[0]
			// Clean up after param interpolation below
		}

		// Resolve params for plan display
		taskCopy := *task
		playbook.ExpandShorthand(&taskCopy)
		resolved, resolveErr := e.interpolateParams(ctx, taskCopy.Params, pctx)
		if resolveErr == nil {
			// Filter out internal params for display
			displayParams := make(map[string]any, len(resolved))
			for k, v := range resolved {
				if !strings.HasPrefix(k, "_") {
					displayParams[k] = v
				}
			}
			pt.Params = displayParams
		}

		// Attempt check for tasks that will run
		if pt.Status == "will_run" && resolveErr == nil && pctx.Connector != nil {
			mod := module.Get(task.Module)
			if checker, ok := mod.(module.Checker); ok {
				// Apply task-level sudo for the check
				playSudo := pctx.Play.Sudo
				taskSudo := task.ShouldSudo(playSudo)
				if taskSudo != playSudo {
					pctx.Connector.SetSudo(taskSudo, pctx.Play.SudoPassword)
				}

				// Inject internal params needed by template/copy
				checkParams := make(map[string]any, len(resolved))
				for k, v := range resolved {
					checkParams[k] = v
				}
				if task.RolePath != "" {
					checkParams["_role_path"] = task.RolePath
				}
				checkParams["_diff_enabled"] = e.Verbose || e.ShowDiff
				if task.Module == "template" {
					checkParams["_template_vars"] = pctx.Vars
					if pctx.SSMParams != nil {
						checkParams["_ssm_params"] = pctx.SSMParams
					}
				}

				cr, err := checker.Check(ctx, pctx.Connector, checkParams)
				if err == nil && cr != nil {
					if cr.Uncertain {
						pt.Status = "always_runs"
						pt.Reason = cr.Message
					} else if cr.WouldChange {
						pt.Status = "will_change"
						pt.Reason = cr.Message
					} else {
						pt.Status = "no_change"
						pt.Reason = cr.Message
					}
					pt.OldChecksum = cr.OldChecksum
					pt.NewChecksum = cr.NewChecksum
					pt.OldContent = cr.OldContent
					pt.NewContent = cr.NewContent
				}
				// On error: silently fall back to "will_run"

				// Restore play-level sudo
				if taskSudo != playSudo {
					pctx.Connector.SetSudo(playSudo, pctx.Play.SudoPassword)
				}
			}
		}

		// Clean up loop variable
		if len(task.Loop) > 0 {
			delete(pctx.Vars, task.GetLoopVar())
		}

		// Track registered variable for subsequent tasks
		if task.Register != "" {
			registeredNames[task.Register] = true
		}

		plan = append(plan, pt)
	}

	return plan
}

// planBlock generates plan entries for a block/rescue/always task group.
func (e *Executor) planBlock(ctx context.Context, pctx *PlayContext, task *playbook.Task, indent int, registeredNames map[string]bool, inheritedBlockTags ...[]string) []output.PlannedTask {
	var parentBlockTags []string
	if len(inheritedBlockTags) > 0 {
		parentBlockTags = inheritedBlockTags[0]
	}

	var plan []output.PlannedTask

	blockName := task.String()
	blockStatus := "will_run"

	// Check block-level tag filtering
	blockETags := effectiveTags(task, pctx.Play.Tags, parentBlockTags)
	if !shouldRunTask(blockETags, e.Tags, e.SkipTags) {
		blockStatus = "will_skip"
	}

	// Check block-level when condition
	if blockStatus == "will_run" && task.When != "" {
		if e.conditionReferencesRegistered(task.When, registeredNames) {
			blockStatus = "conditional"
		} else {
			shouldRun, err := e.evaluateCondition(task.When, pctx)
			if err != nil || !shouldRun {
				blockStatus = "will_skip"
			}
		}
	}

	// Block header
	plan = append(plan, output.PlannedTask{
		Host:      pctx.Host,
		Name:      "BLOCK: " + blockName,
		Status:    blockStatus,
		Indent:    indent,
		IsSection: true,
		Tags:      blockETags,
	})

	if blockStatus == "will_skip" {
		return plan
	}

	// Merge block tags for child tasks
	childBlockTags := mergeBlockTags(parentBlockTags, task.Tags)

	// Plan block tasks
	for _, bt := range task.Block {
		if bt.IsBlock() {
			plan = append(plan, e.planBlock(ctx, pctx, bt, indent+1, registeredNames, childBlockTags)...)
		} else {
			pts := e.planTasks(ctx, pctx, []*playbook.Task{bt}, pctx.Output, childBlockTags)
			for i := range pts {
				pts[i].Indent = indent + 1
			}
			plan = append(plan, pts...)
		}
	}

	// Plan rescue tasks if present
	if len(task.Rescue) > 0 {
		plan = append(plan, output.PlannedTask{
			Host:      pctx.Host,
			Name:      "RESCUE: " + blockName,
			Status:    "conditional",
			Indent:    indent,
			IsSection: true,
		})
		for _, rt := range task.Rescue {
			if rt.IsBlock() {
				plan = append(plan, e.planBlock(ctx, pctx, rt, indent+1, registeredNames, childBlockTags)...)
			} else {
				pts := e.planTasks(ctx, pctx, []*playbook.Task{rt}, pctx.Output, childBlockTags)
				for i := range pts {
					pts[i].Indent = indent + 1
				}
				plan = append(plan, pts...)
			}
		}
	}

	// Plan always tasks if present
	if len(task.Always) > 0 {
		plan = append(plan, output.PlannedTask{
			Host:      pctx.Host,
			Name:      "ALWAYS: " + blockName,
			Status:    "will_run",
			Indent:    indent,
			IsSection: true,
		})
		for _, at := range task.Always {
			if at.IsBlock() {
				plan = append(plan, e.planBlock(ctx, pctx, at, indent+1, registeredNames, childBlockTags)...)
			} else {
				pts := e.planTasks(ctx, pctx, []*playbook.Task{at}, pctx.Output, childBlockTags)
				for i := range pts {
					pts[i].Indent = indent + 1
				}
				plan = append(plan, pts...)
			}
		}
	}

	return plan
}

// conditionReferencesRegistered checks whether a when condition references
// any variable name that was (or will be) populated by a register directive.
func (e *Executor) conditionReferencesRegistered(condition string, registered map[string]bool) bool {
	for name := range registered {
		if strings.Contains(condition, name) {
			return true
		}
	}
	return false
}

// planHandlers produces plan entries for notifiable handlers.
// It uses task definitions and their plan results to determine whether
// any notifying task would actually produce a change.
func (e *Executor) planHandlers(host string, tasks []*playbook.Task, taskPlan []output.PlannedTask, handlers []*playbook.Task) []output.PlannedTask {
	// Build a set of handler names that could potentially be notified.
	// A handler could be notified if at least one task that lists it in
	// notify has a plan status that implies change (will_change, always_runs,
	// will_run, conditional — anything other than no_change / will_skip).
	maybeNotified := make(map[string]bool)
	for i, task := range tasks {
		if len(task.Notify) == 0 {
			continue
		}
		if i >= len(taskPlan) {
			break
		}
		st := taskPlan[i].Status
		if st != "no_change" && st != "will_skip" {
			for _, name := range task.Notify {
				maybeNotified[name] = true
			}
		}
	}

	var plan []output.PlannedTask
	for _, h := range handlers {
		if !maybeNotified[h.Name] {
			continue
		}
		plan = append(plan, output.PlannedTask{
			Host:   host,
			Name:   h.String(),
			Module: h.Module,
			Status: "conditional",
			Reason: "notified",
		})
	}
	return plan
}

// needsSudoPassword prompts for a sudo password when sudo was explicitly
// requested via -s/--sudo on the CLI and no password has been provided yet.
// This runs before any host output so the prompt appears before "PLAY
// <host>".
//
// Playbook- or task-level `sudo: true` alone does NOT trigger the prompt:
// passwordless sudo (NOPASSWD) works transparently, and if a target
// actually requires a password, sudo itself surfaces the error, or the
// caller can supply --sudo-password / TACK_SUDO_PASSWORD explicitly.
func (e *Executor) needsSudoPassword(play *playbook.Play) error {
	// Already have a sudo password
	if play.SudoPassword != "" {
		return nil
	}

	// Only prompt when the user explicitly asked for sudo on the CLI.
	if !e.SudoPromptRequested {
		return nil
	}

	// User explicitly opted out of the prompt (flag / env / non-TTY stdin).
	// If sudo later needs a password, the sudo command itself will error.
	if e.SudoNoPrompt {
		return nil
	}

	// Need a password — prompt for it
	if e.PromptSudoPassword == nil {
		return fmt.Errorf("sudo requires a password; use --sudo-password or configure passwordless sudo")
	}

	pass, err := e.PromptSudoPassword()
	if err != nil {
		return fmt.Errorf("failed to read sudo password: %w", err)
	}

	play.SudoPassword = pass
	return nil
}

// GetConnector returns a connector for the play targeting a specific host.
func (e *Executor) GetConnector(play *playbook.Play, host string) (connector.Connector, error) {
	connType := play.GetConnection()

	switch connType {
	case "local":
		var opts []local.Option
		if play.Sudo {
			opts = append(opts, local.WithSudo())
			if play.SudoPassword != "" {
				opts = append(opts, local.WithSudoPassword(play.SudoPassword))
			}
		}
		return local.New(opts...), nil

	case "docker":
		return docker.New(host), nil

	case "ssh":
		var sshOpts []sshconn.Option
		if play.Sudo {
			sshOpts = append(sshOpts, sshconn.WithSudo())
			if play.SudoPassword != "" {
				sshOpts = append(sshOpts, sshconn.WithSudoPassword(play.SudoPassword))
			}
		}

		// Resolve effective SSH config: play.SSH > inventory host SSH > inventory group SSH.
		// We build a merged view where play settings always win.
		effectiveSSH := mergeSSHConfig(play.SSH, e.Inventory, host)

		if effectiveSSH.User != "" {
			sshOpts = append(sshOpts, sshconn.WithUser(effectiveSSH.User))
		}
		// Check if the host string embeds a port (e.g. "host:2222" from a URI).
		// Embedded port takes priority over all other port settings.
		sshHost := host
		if h, p, err := net.SplitHostPort(host); err == nil {
			sshHost = h
			if pn, err := strconv.Atoi(p); err == nil {
				sshOpts = append(sshOpts, sshconn.WithPort(pn))
			}
		} else if effectiveSSH.Port != 0 {
			sshOpts = append(sshOpts, sshconn.WithPort(effectiveSSH.Port))
		}
		if effectiveSSH.Key != "" {
			sshOpts = append(sshOpts, sshconn.WithKeyFile(effectiveSSH.Key))
		}
		if effectiveSSH.Password != "" {
			sshOpts = append(sshOpts, sshconn.WithPassword(effectiveSSH.Password))
		}
		if effectiveSSH.HostKeyChecking != nil && !*effectiveSSH.HostKeyChecking {
			sshOpts = append(sshOpts, sshconn.WithInsecureHostKey())
		}
		return sshconn.New(sshHost, sshOpts...), nil

	case "ssm":
		var ssmOpts []ssmconn.Option
		if play.Sudo {
			ssmOpts = append(ssmOpts, ssmconn.WithSudo())
			if play.SudoPassword != "" {
				ssmOpts = append(ssmOpts, ssmconn.WithSudoPassword(play.SudoPassword))
			}
		}
		if play.SSM != nil {
			if play.SSM.Region != "" {
				ssmOpts = append(ssmOpts, ssmconn.WithRegion(play.SSM.Region))
			}
			if play.SSM.Bucket != "" {
				ssmOpts = append(ssmOpts, ssmconn.WithBucket(play.SSM.Bucket))
			}
			if play.SSM.AttachS3Policy {
				ssmOpts = append(ssmOpts, ssmconn.WithAutoIAMPolicy())
			}
		}
		return ssmconn.New(host, ssmOpts...), nil

	default:
		return nil, fmt.Errorf("unknown connection type: %s", connType)
	}
}

// evaluateCondition evaluates a when condition using the expression parser.
func (e *Executor) evaluateCondition(condition string, pctx *PlayContext) (bool, error) {
	return evaluateConditionExpr(condition, pctx)
}

// resolveValue resolves a value that might be a variable reference.
func (e *Executor) resolveValue(s string, pctx *PlayContext) any {
	s = strings.TrimSpace(s)

	// String literal
	if len(s) >= 2 &&
		((strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) ||
			(strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\""))) {
		return s[1 : len(s)-1]
	}

	// Boolean literals
	if s == "true" || s == "True" {
		return true
	}
	if s == "false" || s == "False" {
		return false
	}

	// Variable lookup
	if val, ok := pctx.Vars[s]; ok {
		return val
	}

	// Dotted variable lookup (e.g., facts.os)
	if strings.Contains(s, ".") {
		parts := strings.Split(s, ".")
		var current any = pctx.Vars
		for _, part := range parts {
			if m, ok := current.(map[string]any); ok {
				current = m[part]
			} else {
				return nil
			}
		}
		return current
	}

	return s
}

// isTruthy returns whether a value is considered truthy.
func isTruthy(v any) bool {
	if v == nil {
		return false
	}

	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val != "" && val != "false" && val != "False" && val != "no"
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case []any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}

// getEnvMap returns environment variables as a map.
func getEnvMap() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.Index(e, "="); idx > 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}
	return env
}

// allNoChange returns true if every planned task has status "no_change" or "will_skip".
func allNoChange(tasks []output.PlannedTask) bool {
	for _, t := range tasks {
		if t.IsSection {
			continue
		}
		if t.Status != "no_change" && t.Status != "will_skip" {
			return false
		}
	}
	return true
}

// toStringMap converts a value to map[string]string. Supports map[string]string
// directly or map[string]any with string values (from YAML parsing).
func toStringMap(v any) (map[string]string, bool) {
	switch m := v.(type) {
	case map[string]string:
		return m, true
	case map[string]any:
		result := make(map[string]string, len(m))
		for k, val := range m {
			result[k] = fmt.Sprintf("%v", val)
		}
		return result, true
	default:
		return nil, false
	}
}

// mergeSSHConfig returns the effective SSH config for a host by merging
// play-level SSH settings with inventory per-host and group SSH settings.
// Priority: play.SSH > inventory host SSH > inventory group SSH.
// A zero-value field means "not set"; the first non-zero value wins.
func mergeSSHConfig(playCfg *playbook.SSHConfig, inv *inventory.Inventory, host string) playbook.SSHConfig {
	var result playbook.SSHConfig

	// Collect sources from lowest to highest priority so higher-priority
	// values overwrite lower-priority ones.
	var sources []*playbook.SSHConfig

	// Lowest: group SSH
	if inv != nil {
		for _, g := range inv.GetHostGroups(host) {
			if g.SSH != nil {
				sources = append(sources, g.SSH)
			}
		}
	}

	// Middle: per-host inventory SSH
	if inv != nil {
		if entry := inv.GetHost(host); entry != nil && entry.SSH != nil {
			sources = append(sources, entry.SSH)
		}
	}

	// Highest: play-level SSH
	if playCfg != nil {
		sources = append(sources, playCfg)
	}

	for _, src := range sources {
		if src.User != "" {
			result.User = src.User
		}
		if src.Port != 0 {
			result.Port = src.Port
		}
		if src.Key != "" {
			result.Key = src.Key
		}
		if src.Password != "" {
			result.Password = src.Password
		}
		if src.HostKeyChecking != nil {
			result.HostKeyChecking = src.HostKeyChecking
		}
	}

	return result
}
