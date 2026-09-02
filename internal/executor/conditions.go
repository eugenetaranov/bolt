package executor

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Token types for the condition lexer.
type tokenType int

const (
	tokEOF tokenType = iota
	tokAND
	tokOR
	tokNOT
	tokEQ  // ==
	tokNEQ // !=
	tokLT  // <
	tokGT  // >
	tokLTE // <=
	tokGTE // >=
	tokIN
	tokIS
	tokDEFINED
	tokLPAREN   // (
	tokRPAREN   // )
	tokLBRACKET // [
	tokRBRACKET // ]
	tokCOMMA    // ,
	tokSTRING   // 'value' or "value"
	tokNUMBER   // 123 or 1.5
	tokBOOL     // true/false
	tokIDENT    // variable name (may contain dots)
)

type token struct {
	typ tokenType
	val string
}

// lexCondition tokenizes a condition string.
func lexCondition(input string) ([]token, error) {
	var tokens []token
	i := 0

	for i < len(input) {
		// Skip whitespace
		if unicode.IsSpace(rune(input[i])) {
			i++
			continue
		}

		// Two-char operators
		if i+1 < len(input) {
			two := input[i : i+2]
			switch two {
			case "==":
				tokens = append(tokens, token{tokEQ, "=="})
				i += 2
				continue
			case "!=":
				tokens = append(tokens, token{tokNEQ, "!="})
				i += 2
				continue
			case "<=":
				tokens = append(tokens, token{tokLTE, "<="})
				i += 2
				continue
			case ">=":
				tokens = append(tokens, token{tokGTE, ">="})
				i += 2
				continue
			}
		}

		// Single-char operators
		switch input[i] {
		case '<':
			tokens = append(tokens, token{tokLT, "<"})
			i++
			continue
		case '>':
			tokens = append(tokens, token{tokGT, ">"})
			i++
			continue
		case '(':
			tokens = append(tokens, token{tokLPAREN, "("})
			i++
			continue
		case ')':
			tokens = append(tokens, token{tokRPAREN, ")"})
			i++
			continue
		case '[':
			tokens = append(tokens, token{tokLBRACKET, "["})
			i++
			continue
		case ']':
			tokens = append(tokens, token{tokRBRACKET, "]"})
			i++
			continue
		case ',':
			tokens = append(tokens, token{tokCOMMA, ","})
			i++
			continue
		}

		// Quoted strings
		if input[i] == '\'' || input[i] == '"' {
			quote := input[i]
			i++
			start := i
			for i < len(input) && input[i] != quote {
				i++
			}
			if i >= len(input) {
				return nil, fmt.Errorf("unterminated string starting at position %d", start-1)
			}
			tokens = append(tokens, token{tokSTRING, input[start:i]})
			i++ // skip closing quote
			continue
		}

		// Numbers
		if input[i] == '-' || (input[i] >= '0' && input[i] <= '9') {
			// Only treat '-' as number start if followed by digit
			if input[i] == '-' && (i+1 >= len(input) || input[i+1] < '0' || input[i+1] > '9') {
				// Not a number, fall through to ident
			} else {
				start := i
				if input[i] == '-' {
					i++
				}
				for i < len(input) && ((input[i] >= '0' && input[i] <= '9') || input[i] == '.') {
					i++
				}
				// Verify it's not followed by a letter (would be an ident)
				if i < len(input) && (unicode.IsLetter(rune(input[i])) || input[i] == '_') {
					// It's an identifier starting with digits — rewind
					i = start
				} else {
					tokens = append(tokens, token{tokNUMBER, input[start:i]})
					continue
				}
			}
		}

		// Keywords and identifiers
		if unicode.IsLetter(rune(input[i])) || input[i] == '_' {
			start := i
			for i < len(input) && (unicode.IsLetter(rune(input[i])) || unicode.IsDigit(rune(input[i])) || input[i] == '_' || input[i] == '.') {
				i++
			}
			word := input[start:i]

			switch strings.ToLower(word) {
			case "and":
				tokens = append(tokens, token{tokAND, "and"})
			case "or":
				tokens = append(tokens, token{tokOR, "or"})
			case "not":
				// Check for "not in"
				j := i
				for j < len(input) && unicode.IsSpace(rune(input[j])) {
					j++
				}
				if j+2 <= len(input) && strings.ToLower(input[j:j+2]) == "in" && (j+2 >= len(input) || !unicode.IsLetter(rune(input[j+2]))) {
					tokens = append(tokens, token{tokNOT, "not"})
					tokens = append(tokens, token{tokIN, "in"})
					i = j + 2
				} else {
					tokens = append(tokens, token{tokNOT, "not"})
				}
			case "in":
				tokens = append(tokens, token{tokIN, "in"})
			case "is":
				// Check for "is defined" / "is not defined"
				tokens = append(tokens, token{tokIS, "is"})
			case "defined":
				tokens = append(tokens, token{tokDEFINED, "defined"})
			case "true", "True":
				tokens = append(tokens, token{tokBOOL, "true"})
			case "false", "False":
				tokens = append(tokens, token{tokBOOL, "false"})
			default:
				tokens = append(tokens, token{tokIDENT, word})
			}
			continue
		}

		return nil, fmt.Errorf("unexpected character '%c' at position %d", input[i], i)
	}

	tokens = append(tokens, token{tokEOF, ""})
	return tokens, nil
}

// condNode represents a node in the condition AST.
type condNode interface {
	condNode() // marker method
}

type binaryNode struct {
	op    tokenType
	left  condNode
	right condNode
}

type unaryNode struct {
	op      tokenType
	operand condNode
}

type literalNode struct {
	value any // string, float64, bool
}

type identNode struct {
	name string
}

type listNode struct {
	items []condNode
}

// isDefinedNode checks if a variable is defined.
type isDefinedNode struct {
	name    string
	negated bool // true for "is not defined"
}

func (binaryNode) condNode()    {}
func (unaryNode) condNode()     {}
func (literalNode) condNode()   {}
func (identNode) condNode()     {}
func (listNode) condNode()      {}
func (isDefinedNode) condNode() {}

// condParser is a recursive descent parser for condition expressions.
type condParser struct {
	tokens []token
	pos    int
}

func (p *condParser) peek() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return token{tokEOF, ""}
}

func (p *condParser) advance() token {
	t := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return t
}

func (p *condParser) expect(typ tokenType) (token, error) {
	t := p.advance()
	if t.typ != typ {
		return t, fmt.Errorf("expected %d, got %q", typ, t.val)
	}
	return t, nil
}

// parseExpr is the entry point: expr → or_expr
func (p *condParser) parseExpr() (condNode, error) {
	return p.parseOr()
}

// parseOr: or_expr → and_expr ("or" and_expr)*
func (p *condParser) parseOr() (condNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for p.peek().typ == tokOR {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: tokOR, left: left, right: right}
	}
	return left, nil
}

// parseAnd: and_expr → not_expr ("and" not_expr)*
func (p *condParser) parseAnd() (condNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}

	for p.peek().typ == tokAND {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: tokAND, left: left, right: right}
	}
	return left, nil
}

// parseNot: not_expr → "not" not_expr | comparison
func (p *condParser) parseNot() (condNode, error) {
	if p.peek().typ == tokNOT {
		p.advance()
		// Check if next is "in" (already consumed as "not in" by lexer check,
		// but if they come separately, handle it)
		operand, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &unaryNode{op: tokNOT, operand: operand}, nil
	}
	return p.parseComparison()
}

// parseComparison: comparison → primary (op primary)?
func (p *condParser) parseComparison() (condNode, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}

	switch p.peek().typ {
	case tokEQ, tokNEQ, tokLT, tokGT, tokLTE, tokGTE:
		op := p.advance()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return &binaryNode{op: op.typ, left: left, right: right}, nil
	case tokIN:
		p.advance()
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return &binaryNode{op: tokIN, left: left, right: right}, nil
	case tokNOT:
		// "not in"
		p.advance()
		if p.peek().typ == tokIN {
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			return &unaryNode{op: tokNOT, operand: &binaryNode{op: tokIN, left: left, right: right}}, nil
		}
		return nil, fmt.Errorf("expected 'in' after 'not', got %q", p.peek().val)
	case tokIS:
		// "is defined" / "is not defined"
		p.advance()
		negated := false
		if p.peek().typ == tokNOT {
			p.advance()
			negated = true
		}
		if p.peek().typ != tokDEFINED {
			return nil, fmt.Errorf("expected 'defined' after 'is', got %q", p.peek().val)
		}
		p.advance()
		name := ""
		if ident, ok := left.(*identNode); ok {
			name = ident.name
		}
		return &isDefinedNode{name: name, negated: negated}, nil
	}

	return left, nil
}

// parsePrimary: primary → "(" expr ")" | "[" list "]" | literal | ident
func (p *condParser) parsePrimary() (condNode, error) {
	t := p.peek()

	switch t.typ {
	case tokLPAREN:
		p.advance()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRPAREN); err != nil {
			return nil, fmt.Errorf("expected closing ')'")
		}
		return expr, nil

	case tokLBRACKET:
		p.advance()
		var items []condNode
		if p.peek().typ != tokRBRACKET {
			item, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			for p.peek().typ == tokCOMMA {
				p.advance()
				item, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
		}
		if _, err := p.expect(tokRBRACKET); err != nil {
			return nil, fmt.Errorf("expected closing ']'")
		}
		return &listNode{items: items}, nil

	case tokSTRING:
		p.advance()
		return &literalNode{value: t.val}, nil

	case tokNUMBER:
		p.advance()
		if strings.Contains(t.val, ".") {
			f, _ := strconv.ParseFloat(t.val, 64)
			return &literalNode{value: f}, nil
		}
		n, _ := strconv.ParseInt(t.val, 10, 64)
		return &literalNode{value: float64(n)}, nil

	case tokBOOL:
		p.advance()
		return &literalNode{value: t.val == "true"}, nil

	case tokIDENT:
		p.advance()
		return &identNode{name: t.val}, nil

	default:
		return nil, fmt.Errorf("unexpected token %q", t.val)
	}
}

// parseCondition parses a condition string into an AST.
func parseCondition(input string) (condNode, error) {
	tokens, err := lexCondition(input)
	if err != nil {
		return nil, err
	}
	p := &condParser{tokens: tokens}
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().typ != tokEOF {
		return nil, fmt.Errorf("unexpected token %q after expression", p.peek().val)
	}
	return node, nil
}

// condEvaluator evaluates condition AST nodes against a PlayContext.
type condEvaluator struct {
	pctx *PlayContext
}

func (ev *condEvaluator) eval(node condNode) (any, error) {
	switch n := node.(type) {
	case *binaryNode:
		return ev.evalBinary(n)
	case *unaryNode:
		return ev.evalUnary(n)
	case *literalNode:
		return n.value, nil
	case *identNode:
		return ev.resolveIdent(n.name), nil
	case *listNode:
		return ev.evalList(n)
	case *isDefinedNode:
		return ev.evalIsDefined(n)
	default:
		return nil, fmt.Errorf("unknown node type")
	}
}

func (ev *condEvaluator) evalBool(node condNode) (bool, error) {
	val, err := ev.eval(node)
	if err != nil {
		return false, err
	}
	if b, ok := val.(bool); ok {
		return b, nil
	}
	return isTruthy(val), nil
}

func (ev *condEvaluator) evalBinary(n *binaryNode) (any, error) {
	switch n.op {
	case tokAND:
		left, err := ev.evalBool(n.left)
		if err != nil {
			return nil, err
		}
		if !left {
			return false, nil
		}
		return ev.evalBool(n.right)

	case tokOR:
		left, err := ev.evalBool(n.left)
		if err != nil {
			return nil, err
		}
		if left {
			return true, nil
		}
		return ev.evalBool(n.right)

	case tokEQ:
		left, err := ev.eval(n.left)
		if err != nil {
			return nil, err
		}
		right, err := ev.eval(n.right)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%v", left) == fmt.Sprintf("%v", right), nil

	case tokNEQ:
		left, err := ev.eval(n.left)
		if err != nil {
			return nil, err
		}
		right, err := ev.eval(n.right)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%v", left) != fmt.Sprintf("%v", right), nil

	case tokLT, tokGT, tokLTE, tokGTE:
		left, err := ev.eval(n.left)
		if err != nil {
			return nil, err
		}
		right, err := ev.eval(n.right)
		if err != nil {
			return nil, err
		}
		return compareValues(left, right, n.op), nil

	case tokIN:
		left, err := ev.eval(n.left)
		if err != nil {
			return nil, err
		}
		right, err := ev.eval(n.right)
		if err != nil {
			return nil, err
		}
		return evalIn(left, right), nil

	default:
		return nil, fmt.Errorf("unknown binary operator %d", n.op)
	}
}

func (ev *condEvaluator) evalUnary(n *unaryNode) (any, error) {
	val, err := ev.evalBool(n.operand)
	if err != nil {
		return nil, err
	}
	if n.op == tokNOT {
		return !val, nil
	}
	return nil, fmt.Errorf("unknown unary operator %d", n.op)
}

func (ev *condEvaluator) evalList(n *listNode) (any, error) {
	var result []any
	for _, item := range n.items {
		val, err := ev.eval(item)
		if err != nil {
			return nil, err
		}
		result = append(result, val)
	}
	return result, nil
}

func (ev *condEvaluator) evalIsDefined(n *isDefinedNode) (any, error) {
	val := ev.resolveIdent(n.name)
	defined := val != nil
	if n.negated {
		return !defined, nil
	}
	return defined, nil
}

// resolveIdent resolves a variable name against the PlayContext.
func (ev *condEvaluator) resolveIdent(name string) any {
	// Check registered results first (including .changed access)
	if strings.Contains(name, ".") {
		parts := strings.SplitN(name, ".", 2)
		if reg, ok := ev.pctx.Registered[parts[0]]; ok {
			if regMap, ok := reg.(map[string]any); ok {
				if len(parts) > 1 {
					return regMap[parts[1]]
				}
				return reg
			}
			return reg
		}
	}
	if reg, ok := ev.pctx.Registered[name]; ok {
		return reg
	}

	// Check vars
	if val, ok := ev.pctx.Vars[name]; ok {
		return val
	}

	// Dotted variable lookup
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		var current any = ev.pctx.Vars
		for _, part := range parts {
			if m, ok := current.(map[string]any); ok {
				current = m[part]
			} else {
				return nil
			}
		}
		return current
	}

	return nil
}

// compareValues compares two values with smart type coercion.
func compareValues(left, right any, op tokenType) bool {
	// Try numeric comparison
	leftNum, leftOk := toFloat64(left)
	rightNum, rightOk := toFloat64(right)

	if leftOk && rightOk {
		switch op {
		case tokLT:
			return leftNum < rightNum
		case tokGT:
			return leftNum > rightNum
		case tokLTE:
			return leftNum <= rightNum
		case tokGTE:
			return leftNum >= rightNum
		}
	}

	// Fall back to string comparison
	leftStr := fmt.Sprintf("%v", left)
	rightStr := fmt.Sprintf("%v", right)
	switch op {
	case tokLT:
		return leftStr < rightStr
	case tokGT:
		return leftStr > rightStr
	case tokLTE:
		return leftStr <= rightStr
	case tokGTE:
		return leftStr >= rightStr
	}
	return false
}

// toFloat64 attempts to convert a value to float64.
func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	}
	return 0, false
}

// evalIn checks if left is contained in right. For a list, this is membership;
// for a string, this is a substring test (matching Ansible's `'x' in stdout`).
func evalIn(left, right any) bool {
	leftStr := fmt.Sprintf("%v", left)

	switch r := right.(type) {
	case []any:
		for _, item := range r {
			if fmt.Sprintf("%v", item) == leftStr {
				return true
			}
		}
	case []string:
		for _, item := range r {
			if item == leftStr {
				return true
			}
		}
	case string:
		return strings.Contains(r, leftStr)
	}
	return false
}

// evaluateConditionExpr parses and evaluates a condition expression.
func evaluateConditionExpr(condition string, pctx *PlayContext) (bool, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true, nil
	}

	node, err := parseCondition(condition)
	if err != nil {
		return false, fmt.Errorf("failed to parse condition %q: %w", condition, err)
	}

	ev := &condEvaluator{pctx: pctx}
	val, err := ev.evalBool(node)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate condition %q: %w", condition, err)
	}
	return val, nil
}
