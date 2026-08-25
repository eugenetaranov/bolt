package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/output"
	"github.com/tackhq/tack/internal/playbook"
	"github.com/tackhq/tack/pkg/facts"
)

// factsConcurrency caps concurrent fact-gather goroutines independent of
// --forks. Set to a value that fits typical SSM API ceilings; large enough
// to make small-fleet runs effectively unbounded, small enough to avoid
// throttling on hundreds of hosts.
const factsConcurrency = 20

// hostPrep carries the result of the discovery pre-pass for one host:
// the open connector (reused by the apply phase), the gathered facts,
// any error, and a buffered emitter capturing the pre-pass output for
// host-ordered flush.
//
// The richer fields (pctx, allTasks, allHandlers, planned) are populated
// by discoverAndPlanParallel when the multi-host orchestrator needs the
// pre-pass to also compute the per-host plan; gatherFactsParallel leaves
// them zero.
type hostPrep struct {
	host   string
	conn   connector.Connector
	facts  map[string]any
	err    error
	output *bytes.Buffer

	// Plan-stage fields (populated by discoverAndPlanParallel only).
	pctx        *PlayContext
	allTasks    []*playbook.Task
	allHandlers []*playbook.Task
	planned     []output.PlannedTask
}

// gatherFactsParallel runs facts.Gather concurrently across all hosts.
// Returns a map keyed by host name. The caller is responsible for flushing
// per-host output buffers in order and for closing connectors of failed
// hosts (successful hosts hand their connector off to runPlayOnHost which
// closes it after apply).
//
// Returns nil when the pre-pass should be skipped — caller falls back to
// today's inline gather path:
//   - len(play.Hosts) <= 1 (nothing to parallelize)
//   - play.GetConnection() == "local" (single localhost run)
//   - !play.ShouldGatherFacts() (gather disabled)
func (e *Executor) gatherFactsParallel(ctx context.Context, play *playbook.Play) map[string]*hostPrep {
	hosts := play.Hosts
	if play.GetConnection() == "local" || len(hosts) <= 1 || !e.shouldGatherFacts(play) {
		return nil
	}

	limit := factsConcurrency
	if len(hosts) < limit {
		limit = len(hosts)
	}

	start := time.Now()
	if e.Output != nil {
		e.Output.Debug("parallel fact gather: %d hosts, concurrency=%d", len(hosts), limit)
	}

	var (
		mu      sync.Mutex
		results = make(map[string]*hostPrep, len(hosts))
	)

	pool := NewWorkerPool(limit)
	for _, host := range hosts {
		host := host
		pool.Submit(ctx, func(ctx context.Context) *HostResult {
			prep := &hostPrep{host: host, output: &bytes.Buffer{}}

			hostOutput := output.New(prep.output)
			if textOut, ok := e.Output.(*output.Output); ok {
				hostOutput.SetColor(textOut.ColorEnabled())
			}
			hostOutput.SetDebug(e.Debug)
			hostOutput.SetVerbose(e.Verbose)
			hostOutput.SetDiff(e.ShowDiff)

			hostOutput.HostStart(host, play.GetConnection())

			factory := e.connectorFactory
			if factory == nil {
				factory = e.GetConnector
			}
			conn, err := factory(play, host)
			if err != nil {
				prep.err = fmt.Errorf("failed to create connector: %w", err)
				hostOutput.HostFactsResult(host, false, prep.err.Error())
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}

			if err := conn.Connect(ctx); err != nil {
				prep.err = fmt.Errorf("failed to connect: %w", err)
				hostOutput.HostFactsResult(host, false, prep.err.Error())
				_ = conn.Close()
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}

			f, err := facts.Gather(ctx, conn)
			if err != nil {
				prep.err = fmt.Errorf("failed to gather facts: %w", err)
				hostOutput.HostFactsResult(host, false, err.Error())
				_ = conn.Close()
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}

			prep.conn = conn
			prep.facts = f
			hostOutput.HostFactsResult(host, true, "")

			mu.Lock()
			results[host] = prep
			mu.Unlock()
			return &HostResult{Host: host, Success: true}
		})
	}
	pool.Wait()

	if e.Output != nil {
		var failed int
		for _, prep := range results {
			if prep != nil && prep.err != nil {
				failed++
			}
		}
		e.Output.Debug("parallel fact gather complete: %d ok, %d failed in %s",
			len(results)-failed, failed, time.Since(start).Round(time.Millisecond))
	}

	return results
}

// flushPrepBuffers writes per-host pre-pass output to w in hosts-slice order,
// regardless of which goroutine finished first.
func flushPrepBuffers(w io.Writer, hosts []string, preps map[string]*hostPrep) {
	for _, host := range hosts {
		prep, ok := preps[host]
		if !ok || prep.output == nil {
			continue
		}
		data := prep.output.Bytes()
		if len(data) > 0 {
			fmt.Fprint(w, string(data))
		}
	}
}

// closePrepConnectors closes connectors held by successful preps. Used when
// the play exits before apply (dry-run, no-changes, error in another host
// under serial mode, etc.) so we don't leak connections.
func closePrepConnectors(preps map[string]*hostPrep) {
	for _, prep := range preps {
		if prep != nil && prep.conn != nil {
			_ = prep.conn.Close()
			prep.conn = nil
		}
	}
}

// discoverAndPlanParallel runs discovery (connector + facts) AND plan
// computation for every host in parallel. Each prep returned carries an
// open connector, the facts map, a fully-populated PlayContext, and the
// per-host []PlannedTask. Callers aggregate plans across hosts, render
// once, prompt once, then dispatch apply.
//
// Returns nil under the same conditions as gatherFactsParallel — local
// connection, single-host plays, or empty hosts list. (Unlike
// gatherFactsParallel, this method runs even when gather_facts is
// disabled, because the plan is still needed.)
func (e *Executor) discoverAndPlanParallel(ctx context.Context, play *playbook.Play, roles []*playbook.Role, playbookDir string) map[string]*hostPrep {
	hosts := play.Hosts
	if play.GetConnection() == "local" || len(hosts) <= 1 {
		return nil
	}

	limit := factsConcurrency
	if len(hosts) < limit {
		limit = len(hosts)
	}

	start := time.Now()
	if e.Output != nil {
		e.Output.Debug("parallel discover+plan: %d hosts, concurrency=%d", len(hosts), limit)
	}

	var (
		mu      sync.Mutex
		results = make(map[string]*hostPrep, len(hosts))
		done    int64
	)

	// Live progress during the otherwise-silent parallel gather+plan phase
	// (especially valuable for high-latency connectors like SSM). No-op in
	// non-interactive mode.
	stopProgress := func() {}
	if o, ok := e.Output.(*output.Output); ok {
		total := len(hosts)
		stopProgress = o.StartProgress(func() string {
			return fmt.Sprintf("gathering facts + planning %d/%d hosts", atomic.LoadInt64(&done), total)
		})
	}

	pool := NewWorkerPool(limit)
	for _, host := range hosts {
		host := host
		pool.Submit(ctx, func(ctx context.Context) *HostResult {
			defer atomic.AddInt64(&done, 1)
			prep := &hostPrep{host: host, output: &bytes.Buffer{}}

			hostOutput := output.New(prep.output)
			if textOut, ok := e.Output.(*output.Output); ok {
				hostOutput.SetColor(textOut.ColorEnabled())
			}
			hostOutput.SetDebug(e.Debug)
			hostOutput.SetVerbose(e.Verbose)
			hostOutput.SetDiff(e.ShowDiff)

			hostOutput.HostStart(host, play.GetConnection())

			// Open connector and gather facts.
			factory := e.connectorFactory
			if factory == nil {
				factory = e.GetConnector
			}
			conn, err := factory(play, host)
			if err != nil {
				prep.err = fmt.Errorf("failed to create connector: %w", err)
				hostOutput.HostFactsResult(host, false, prep.err.Error())
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}
			if err := conn.Connect(ctx); err != nil {
				prep.err = fmt.Errorf("failed to connect: %w", err)
				hostOutput.HostFactsResult(host, false, prep.err.Error())
				_ = conn.Close()
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}
			prep.conn = conn

			if e.shouldGatherFacts(play) {
				f, gerr := facts.Gather(ctx, conn)
				if gerr != nil {
					prep.err = fmt.Errorf("failed to gather facts: %w", gerr)
					hostOutput.HostFactsResult(host, false, gerr.Error())
					_ = conn.Close()
					prep.conn = nil
					mu.Lock()
					results[host] = prep
					mu.Unlock()
					return &HostResult{Host: host, Success: false, Error: prep.err}
				}
				prep.facts = f
				hostOutput.HostFactsResult(host, true, "")
			} else {
				hostOutput.HostStartDone(host)
			}

			// Build PlayContext (reuses prep.conn + prep.facts).
			pctx, perr := e.preparePlayContext(ctx, play, roles, host, playbookDir, hostOutput, prep)
			if perr != nil {
				prep.err = perr
				_ = conn.Close()
				prep.conn = nil
				mu.Lock()
				results[host] = prep
				mu.Unlock()
				return &HostResult{Host: host, Success: false, Error: prep.err}
			}
			prep.pctx = pctx

			// Compute plan for this host.
			prep.allTasks = playbook.ExpandRoleTasks(roles, play.Tasks)
			prep.allHandlers = playbook.ExpandRoleHandlers(roles, play.Handlers)
			if !e.skipPlanPhase() {
				prep.planned = e.computeHostPlan(ctx, pctx, prep.allTasks, prep.allHandlers)
			}

			mu.Lock()
			results[host] = prep
			mu.Unlock()
			return &HostResult{Host: host, Success: true}
		})
	}
	pool.Wait()
	stopProgress()

	if e.Output != nil {
		var failed int
		for _, prep := range results {
			if prep != nil && prep.err != nil {
				failed++
			}
		}
		e.Output.Debug("parallel discover+plan complete: %d ok, %d failed in %s",
			len(results)-failed, failed, time.Since(start).Round(time.Millisecond))
	}

	return results
}
