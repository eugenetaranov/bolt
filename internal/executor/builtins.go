package executor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tackhq/tack/internal/playbook"
)

// executeSetFact sets one or more variables into the play vars, with values
// interpolated. It never touches the connector and reports ok (not changed).
func (e *Executor) executeSetFact(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	name := builtinTaskName(task, "set_fact")
	pctx.Output.TaskStart(name, "set_fact")

	resolved, err := e.interpolateParams(ctx, task.SetFact, pctx)
	if err != nil {
		pctx.Output.TaskResult(name, "failed", false, err.Error(), eTags)
		return &TaskResult{Status: "failed", Error: err}, err
	}

	keys := make([]string, 0, len(resolved))
	for k, v := range resolved {
		pctx.Vars[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if task.Register != "" {
		reg := map[string]any{"changed": false, "failed": false}
		pctx.Registered[task.Register] = reg
		pctx.Vars[task.Register] = reg
	}

	pctx.Output.TaskResult(name, "ok", false, "set "+strings.Join(keys, ", "), eTags)
	return &TaskResult{Status: "ok", Changed: false}, nil
}

// executeDebug prints a message or the value of a variable. Reports ok.
func (e *Executor) executeDebug(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	name := builtinTaskName(task, "debug")
	pctx.Output.TaskStart(name, "debug")

	spec := task.Debug
	var msg string
	switch {
	case spec.Var != "":
		// Resolve the variable (supports dotted paths) via interpolation.
		val, err := e.interpolateString(ctx, "{{ "+spec.Var+" }}", pctx)
		if err != nil {
			pctx.Output.TaskResult(name, "failed", false, err.Error(), eTags)
			return &TaskResult{Status: "failed", Error: err}, err
		}
		msg = fmt.Sprintf("%s: %v", spec.Var, val)
	case spec.Msg != "":
		val, err := e.interpolateString(ctx, spec.Msg, pctx)
		if err != nil {
			pctx.Output.TaskResult(name, "failed", false, err.Error(), eTags)
			return &TaskResult{Status: "failed", Error: err}, err
		}
		msg = fmt.Sprintf("%v", val)
	default:
		msg = "Hello from debug"
	}

	pctx.Output.TaskResult(name, "ok", false, msg, eTags)
	pctx.Output.Info("%s", msg)
	return &TaskResult{Status: "ok", Changed: false}, nil
}

// executeFail fails the play with an (interpolated) message. Typically gated
// with when:. Reports failed so block/rescue semantics apply normally.
func (e *Executor) executeFail(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	name := builtinTaskName(task, "fail")
	pctx.Output.TaskStart(name, "fail")

	msg := "Failed as requested from fail task"
	if task.Fail.Msg != "" {
		val, err := e.interpolateString(ctx, task.Fail.Msg, pctx)
		if err != nil {
			pctx.Output.TaskResult(name, "failed", false, err.Error(), eTags)
			return &TaskResult{Status: "failed", Error: err}, err
		}
		msg = fmt.Sprintf("%v", val)
	}

	pctx.Output.TaskResult(name, "failed", false, msg, eTags)
	err := fmt.Errorf("%s", msg)
	return &TaskResult{Status: "failed", Error: err}, err
}

// executeMeta handles meta directives. Currently supports flush_handlers, which
// runs pending notified handlers immediately (and clears them so they don't run
// again at end of play).
func (e *Executor) executeMeta(ctx context.Context, pctx *PlayContext, task *playbook.Task, eTags []string) (*TaskResult, error) {
	name := builtinTaskName(task, task.String())

	switch task.Meta {
	case "flush_handlers":
		if len(pctx.NotifiedHandlers) > 0 && pctx.Stats != nil {
			if err := e.runHandlersExpanded(ctx, pctx, pctx.Stats, pctx.ExpandedHandlers); err != nil {
				return &TaskResult{Status: "failed", Error: err}, err
			}
			// Clear so the end-of-play flush doesn't re-run them.
			for k := range pctx.NotifiedHandlers {
				delete(pctx.NotifiedHandlers, k)
			}
		}
		return &TaskResult{Status: "ok", Changed: false}, nil
	default:
		err := fmt.Errorf("meta: unsupported value %q", task.Meta)
		pctx.Output.TaskResult(name, "failed", false, err.Error(), eTags)
		return &TaskResult{Status: "failed", Error: err}, err
	}
}

// builtinTaskName returns the display name for a built-in task.
func builtinTaskName(task *playbook.Task, fallback string) string {
	if task.Name != "" {
		return task.Name
	}
	return fallback
}

// builtinModuleName returns the pseudo-module label used in plan output for a
// built-in task.
func builtinModuleName(task *playbook.Task) string {
	switch {
	case task.IsSetFact():
		return "set_fact"
	case task.IsDebug():
		return "debug"
	case task.IsFail():
		return "fail"
	case task.IsMeta():
		return "meta"
	default:
		return task.Module
	}
}
