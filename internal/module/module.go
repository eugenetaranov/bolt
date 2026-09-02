// Package module defines the interface for idempotent system operations.
package module

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/tackhq/tack/internal/connector"
)

// Result holds the outcome of a module execution.
type Result struct {
	// Changed indicates whether the module made any changes to the system.
	Changed bool

	// Message is a human-readable description of what happened.
	Message string

	// Data holds any additional output data from the module.
	Data map[string]any
}

// DataError is an error that also carries structured result data (for example a
// command's cmd/stdout/stderr/exit_code). The executor uses it to evaluate
// failed_when / changed_when expressions and to populate `register` even when a
// module reports failure — so, e.g., `failed_when: false` can capture a
// non-zero command exit without aborting the play.
type DataError interface {
	error
	ResultData() map[string]any
}

// Module is the interface that all modules must implement.
type Module interface {
	// Name returns the module's unique identifier.
	Name() string

	// Run executes the module with the given parameters.
	// It should be idempotent - running it multiple times with the same
	// parameters should have the same effect as running it once.
	Run(ctx context.Context, conn connector.Connector, params map[string]any) (*Result, error)
}

// registry holds all registered modules.
var (
	registry   = make(map[string]Module)
	registryMu sync.RWMutex
)

// Register adds a module to the registry.
// It panics if a module with the same name is already registered.
func Register(m Module) {
	registryMu.Lock()
	defer registryMu.Unlock()

	name := m.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("module %q is already registered", name))
	}
	registry[name] = m
}

// Get retrieves a module from the registry by name.
// Returns nil if the module is not found.
func Get(name string) Module {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// List returns the names of all registered modules.
func List() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ParamDoc describes a module parameter for documentation.
type ParamDoc struct {
	Name        string
	Type        string
	Required    bool
	Default     string
	Description string
}

// Describer is an optional interface for modules that provide documentation.
type Describer interface {
	Description() string
	Parameters() []ParamDoc
}

// Exampler is an optional interface for modules that provide a usage example.
// Example returns a ready-to-paste YAML task snippet (a single list item, e.g.
// an "- name: ..." block followed by the module's params) shown by the
// `tack module <name>` command.
type Exampler interface {
	Example() string
}

// CheckResult describes what a module would do without making changes.
type CheckResult struct {
	WouldChange bool
	Uncertain   bool   // true when change status can't be determined (e.g. command)
	Message     string

	// Optional fields for content comparison (populated by copy/template modules).
	OldChecksum string
	NewChecksum string
	OldContent  string
	NewContent  string
}

// Checker is an optional interface for check/dry-run support.
// Check() must be read-only — it queries remote state but MUST NOT modify it.
//
// Check() must be safe for concurrent calls across hosts. The multi-host
// orchestration runs Check() in per-host goroutines during the discovery
// pre-pass; each goroutine has its own Connector instance so connector
// state isn't shared, but module implementations must avoid package-level
// mutable state, shared maps without synchronization, or temp-file paths
// that could collide across hosts.
type Checker interface {
	Check(ctx context.Context, conn connector.Connector, params map[string]any) (*CheckResult, error)
}

// WouldChange creates a CheckResult indicating changes are needed.
func WouldChange(msg string) *CheckResult {
	return &CheckResult{WouldChange: true, Message: msg}
}

// NoChange creates a CheckResult indicating no changes are needed.
func NoChange(msg string) *CheckResult {
	return &CheckResult{WouldChange: false, Message: msg}
}

// UncertainChange creates a CheckResult indicating change status can't be determined.
func UncertainChange(msg string) *CheckResult {
	return &CheckResult{Uncertain: true, Message: msg}
}

// Helper functions for creating results

// Changed creates a Result indicating a change was made.
func Changed(msg string) *Result {
	return &Result{Changed: true, Message: msg}
}

// Unchanged creates a Result indicating no change was needed.
func Unchanged(msg string) *Result {
	return &Result{Changed: false, Message: msg}
}

// ChangedWithData creates a Result with a change and additional data.
func ChangedWithData(msg string, data map[string]any) *Result {
	return &Result{Changed: true, Message: msg, Data: data}
}

// EmitResult holds the output of a module's shell emission for export.
type EmitResult struct {
	// Supported indicates whether this module can emit shell for the given params.
	Supported bool

	// Reason explains why a module is unsupported (when Supported=false).
	Reason string

	// Shell is the bash fragment to emit (multiline OK); must be set -e safe.
	Shell string

	// PreHook is optional setup emitted before the task block (deduplicated across blocks).
	PreHook string

	// Warnings holds non-fatal caveats (e.g., "template mode inferred").
	Warnings []string
}

// Emitter is an optional interface for modules that support shell export.
// Modules implementing this interface can have their operations compiled into
// standalone bash scripts via `tack export`.
type Emitter interface {
	Emit(params map[string]any, vars map[string]any) (*EmitResult, error)
}
