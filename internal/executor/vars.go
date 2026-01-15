package executor

import (
	"fmt"
	"regexp"
	"strings"
)

// varPattern matches {{ variable }} syntax.
var varPattern = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// interpolateParams recursively interpolates variables in task parameters.
func (e *Executor) interpolateParams(params map[string]any, pctx *PlayContext) (map[string]any, error) {
	result := make(map[string]any)

	for k, v := range params {
		interpolated, err := e.interpolateValue(v, pctx)
		if err != nil {
			return nil, fmt.Errorf("parameter '%s': %w", k, err)
		}
		result[k] = interpolated
	}

	return result, nil
}

// interpolateValue interpolates variables in a single value.
func (e *Executor) interpolateValue(v any, pctx *PlayContext) (any, error) {
	switch val := v.(type) {
	case string:
		return e.interpolateString(val, pctx)

	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			interpolated, err := e.interpolateValue(item, pctx)
			if err != nil {
				return nil, err
			}
			result[i] = interpolated
		}
		return result, nil

	case map[string]any:
		result := make(map[string]any)
		for k, item := range val {
			interpolated, err := e.interpolateValue(item, pctx)
			if err != nil {
				return nil, err
			}
			result[k] = interpolated
		}
		return result, nil

	default:
		return v, nil
	}
}

// interpolateString replaces {{ var }} patterns with their values.
func (e *Executor) interpolateString(s string, pctx *PlayContext) (any, error) {
	// Check if the entire string is a single variable reference
	// In this case, return the actual value (not stringified)
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") {
		inner := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		if !strings.Contains(inner, "{{") {
			// Single variable reference - return actual value
			val, err := e.resolveVariable(inner, pctx)
			if err != nil {
				return nil, err
			}
			return val, nil
		}
	}

	// Multiple variables or mixed content - stringify all values
	result := varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name
		inner := varPattern.FindStringSubmatch(match)
		if len(inner) < 2 {
			return match
		}

		varExpr := strings.TrimSpace(inner[1])
		val, err := e.resolveVariable(varExpr, pctx)
		if err != nil {
			return match // Keep original on error
		}

		return fmt.Sprintf("%v", val)
	})

	return result, nil
}

// resolveVariable resolves a variable expression.
func (e *Executor) resolveVariable(expr string, pctx *PlayContext) (any, error) {
	expr = strings.TrimSpace(expr)

	// Handle filters (e.g., var | default('value'))
	if idx := strings.Index(expr, "|"); idx > 0 {
		varName := strings.TrimSpace(expr[:idx])
		filter := strings.TrimSpace(expr[idx+1:])
		return e.applyFilter(varName, filter, pctx)
	}

	// Simple variable or dotted path
	return e.lookupVariable(expr, pctx), nil
}

// lookupVariable looks up a variable by name or dotted path.
func (e *Executor) lookupVariable(name string, pctx *PlayContext) any {
	// Check registered results first
	if val, ok := pctx.Registered[name]; ok {
		return val
	}

	// Check vars
	if val, ok := pctx.Vars[name]; ok {
		return val
	}

	// Handle dotted paths (e.g., facts.os_family, env.HOME)
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		var current any = pctx.Vars

		for _, part := range parts {
			switch c := current.(type) {
			case map[string]any:
				current = c[part]
			case map[string]string:
				current = c[part]
			default:
				return nil
			}

			if current == nil {
				return nil
			}
		}

		return current
	}

	return nil
}

// applyFilter applies a filter to a value.
func (e *Executor) applyFilter(varName, filter string, pctx *PlayContext) (any, error) {
	val := e.lookupVariable(varName, pctx)

	// Parse filter name and arguments
	filterName := filter
	var filterArg string

	if idx := strings.Index(filter, "("); idx > 0 {
		filterName = strings.TrimSpace(filter[:idx])
		argPart := filter[idx+1:]
		if endIdx := strings.LastIndex(argPart, ")"); endIdx > 0 {
			filterArg = strings.TrimSpace(argPart[:endIdx])
			// Remove quotes from argument
			filterArg = strings.Trim(filterArg, "'\"")
		}
	}

	switch filterName {
	case "default":
		if val == nil || val == "" {
			return filterArg, nil
		}
		return val, nil

	case "lower":
		if s, ok := val.(string); ok {
			return strings.ToLower(s), nil
		}
		return val, nil

	case "upper":
		if s, ok := val.(string); ok {
			return strings.ToUpper(s), nil
		}
		return val, nil

	case "trim":
		if s, ok := val.(string); ok {
			return strings.TrimSpace(s), nil
		}
		return val, nil

	case "bool":
		return isTruthy(val), nil

	case "string":
		return fmt.Sprintf("%v", val), nil

	case "int":
		switch v := val.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			return int(v), nil
		case string:
			var i int
			_, _ = fmt.Sscanf(v, "%d", &i)
			return i, nil
		}
		return 0, nil

	case "first":
		if slice, ok := val.([]any); ok && len(slice) > 0 {
			return slice[0], nil
		}
		return nil, nil

	case "last":
		if slice, ok := val.([]any); ok && len(slice) > 0 {
			return slice[len(slice)-1], nil
		}
		return nil, nil

	case "length", "count":
		switch v := val.(type) {
		case string:
			return len(v), nil
		case []any:
			return len(v), nil
		case map[string]any:
			return len(v), nil
		}
		return 0, nil

	case "join":
		if slice, ok := val.([]any); ok {
			sep := filterArg
			if sep == "" {
				sep = ","
			}
			var parts []string
			for _, item := range slice {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
			return strings.Join(parts, sep), nil
		}
		return val, nil

	default:
		return nil, fmt.Errorf("unknown filter: %s", filterName)
	}
}
