package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const maxExprDepth = 128

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokString
	tokNumber
	tokIdent
	tokStar
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokDot
	tokComma
	tokBang
	tokOr
	tokAnd
	tokEq
	tokNe
	tokLt
	tokLe
	tokGt
	tokGe
)

type token struct {
	kind tokenKind
	val  string
	num  float64
	pos  int
}

type exprNumber struct {
	val float64
	raw string
}

type exprNode interface {
	isExprNode()
}

type literalExpr struct {
	val any
}

func (literalExpr) isExprNode() {}

type identExpr struct {
	name string
}

func (identExpr) isExprNode() {}

type memberExpr struct {
	target exprNode
	prop   string
}

func (memberExpr) isExprNode() {}

type indexExpr struct {
	target exprNode
	index  exprNode
}

func (indexExpr) isExprNode() {}

type callExpr struct {
	name string
	args []exprNode
}

func (callExpr) isExprNode() {}

type unaryExpr struct {
	op      string
	operand exprNode
}

func (unaryExpr) isExprNode() {}

type binaryExpr struct {
	op    string
	left  exprNode
	right exprNode
}

func (binaryExpr) isExprNode() {}

func tokenizeExpression(src string) ([]token, error) {
	var tokens []token
	i := 0
	n := len(src)

	for i < n {
		ch := src[i]
		if unicode.IsSpace(rune(ch)) {
			i++
			continue
		}

		start := i
		switch ch {
		case '\'':
			i++
			var sb strings.Builder
			closed := false
			for i < n {
				if src[i] == '\'' {
					if i+1 < n && src[i+1] == '\'' {
						sb.WriteByte('\'')
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				sb.WriteByte(src[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string literal at position %d", start)
			}
			tokens = append(tokens, token{kind: tokString, val: sb.String(), pos: start})

		case '(':
			tokens = append(tokens, token{kind: tokLParen, val: "(", pos: start})
			i++
		case ')':
			tokens = append(tokens, token{kind: tokRParen, val: ")", pos: start})
			i++
		case '[':
			tokens = append(tokens, token{kind: tokLBracket, val: "[", pos: start})
			i++
		case ']':
			tokens = append(tokens, token{kind: tokRBracket, val: "]", pos: start})
			i++
		case '.':
			tokens = append(tokens, token{kind: tokDot, val: ".", pos: start})
			i++
		case ',':
			tokens = append(tokens, token{kind: tokComma, val: ",", pos: start})
			i++
		case '*':
			tokens = append(tokens, token{kind: tokStar, val: "*", pos: start})
			i++

		case '|':
			if i+1 < n && src[i+1] == '|' {
				tokens = append(tokens, token{kind: tokOr, val: "||", pos: start})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected character '|' at position %d", start)
			}
		case '&':
			if i+1 < n && src[i+1] == '&' {
				tokens = append(tokens, token{kind: tokAnd, val: "&&", pos: start})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected character '&' at position %d", start)
			}
		case '=':
			if i+1 < n && src[i+1] == '=' {
				tokens = append(tokens, token{kind: tokEq, val: "==", pos: start})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected character '=' at position %d", start)
			}
		case '!':
			if i+1 < n && src[i+1] == '=' {
				tokens = append(tokens, token{kind: tokNe, val: "!=", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokBang, val: "!", pos: start})
				i++
			}
		case '<':
			if i+1 < n && src[i+1] == '=' {
				tokens = append(tokens, token{kind: tokLe, val: "<=", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokLt, val: "<", pos: start})
				i++
			}
		case '>':
			if i+1 < n && src[i+1] == '=' {
				tokens = append(tokens, token{kind: tokGe, val: ">=", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokGt, val: ">", pos: start})
				i++
			}

		default:
			if isDigit(ch) || (ch == '-' && i+1 < n && isDigit(src[i+1])) {
				tok, nextIdx, err := scanNumber(src, i)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, tok)
				i = nextIdx
			} else if isIdentStart(ch) {
				i++
				for i < n && isIdentPart(src[i]) {
					i++
				}
				tokens = append(tokens, token{kind: tokIdent, val: src[start:i], pos: start})
			} else {
				return nil, fmt.Errorf("unexpected character %q at position %d", ch, start)
			}
		}
	}

	tokens = append(tokens, token{kind: tokEOF, pos: n})
	return tokens, nil
}

func scanNumber(src string, start int) (token, int, error) {
	i := start
	n := len(src)
	if src[i] == '-' {
		i++
	}

	// Hex literal: 0x... or -0x...
	if i+1 < n && src[i] == '0' && (src[i+1] == 'x' || src[i+1] == 'X') {
		i += 2
		hexStart := i
		for i < n && isHexDigit(src[i]) {
			i++
		}
		if i == hexStart {
			return token{}, 0, fmt.Errorf("invalid hex literal at position %d", start)
		}
		raw := src[start:i]
		v, err := strconv.ParseInt(raw, 0, 64)
		if err != nil {
			return token{}, 0, fmt.Errorf("invalid hex literal %q: %w", raw, err)
		}
		return token{kind: tokNumber, val: raw, num: float64(v), pos: start}, i, nil
	}

	for i < n && isDigit(src[i]) {
		i++
	}
	if i+1 < n && src[i] == '.' && isDigit(src[i+1]) {
		i++
		for i < n && isDigit(src[i]) {
			i++
		}
	}
	if i < n && (src[i] == 'e' || src[i] == 'E') {
		expPos := i + 1
		if expPos < n && (src[expPos] == '+' || src[expPos] == '-') {
			expPos++
		}
		if expPos < n && isDigit(src[expPos]) {
			i = expPos
			for i < n && isDigit(src[i]) {
				i++
			}
		}
	}

	raw := src[start:i]
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return token{}, 0, fmt.Errorf("invalid number literal %q: %w", raw, err)
	}
	return token{kind: tokNumber, val: raw, num: v, pos: start}, i, nil
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isHexDigit(ch byte) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || isDigit(ch) || ch == '-'
}

type exprParser struct {
	tokens []token
	pos    int
	depth  int
}

func parseExpression(src string) (exprNode, error) {
	tokens, err := tokenizeExpression(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{tokens: tokens}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("unexpected token %q at position %d", p.peek().val, p.peek().pos)
	}
	return node, nil
}

func (p *exprParser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{kind: tokEOF}
	}
	return p.tokens[p.pos]
}

func (p *exprParser) advance() token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *exprParser) enter() error {
	p.depth++
	if p.depth > maxExprDepth {
		return fmt.Errorf("expression exceeds maximum nesting depth %d", maxExprDepth)
	}
	return nil
}

func (p *exprParser) exit() {
	p.depth--
}

func (p *exprParser) parseOr() (exprNode, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.exit()

	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		op := p.advance().val
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (exprNode, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		op := p.advance().val
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseEquality() (exprNode, error) {
	left, err := p.parseRelational()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().kind
		if k != tokEq && k != tokNe {
			break
		}
		op := p.advance().val
		right, err := p.parseRelational()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseRelational() (exprNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().kind
		if k != tokLt && k != tokLe && k != tokGt && k != tokGe {
			break
		}
		op := p.advance().val
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = binaryExpr{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseUnary() (exprNode, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.exit()

	if p.peek().kind == tokBang {
		op := p.advance().val
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return unaryExpr{op: op, operand: operand}, nil
	}
	return p.parsePostfix()
}

func (p *exprParser) parsePostfix() (exprNode, error) {
	node, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tokDot:
			p.advance()
			next := p.peek()
			if next.kind != tokIdent && next.kind != tokStar {
				return nil, fmt.Errorf("expected property name after '.' at position %d", next.pos)
			}
			p.advance()
			node = memberExpr{target: node, prop: next.val}
		case tokLBracket:
			p.advance()
			idxExpr, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if p.peek().kind != tokRBracket {
				return nil, fmt.Errorf("expected ']' at position %d", p.peek().pos)
			}
			p.advance()
			node = indexExpr{target: node, index: idxExpr}
		default:
			return node, nil
		}
	}
}

func (p *exprParser) parsePrimary() (exprNode, error) {
	tok := p.peek()
	switch tok.kind {
	case tokLParen:
		p.advance()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("expected ')' at position %d", p.peek().pos)
		}
		p.advance()
		return inner, nil

	case tokString:
		p.advance()
		return literalExpr{val: tok.val}, nil

	case tokNumber:
		p.advance()
		return literalExpr{val: exprNumber{val: tok.num, raw: tok.val}}, nil

	case tokIdent:
		p.advance()
		if p.peek().kind == tokLParen {
			p.advance()
			var args []exprNode
			if p.peek().kind != tokRParen {
				for {
					arg, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					args = append(args, arg)
					if p.peek().kind != tokComma {
						break
					}
					p.advance()
				}
			}
			if p.peek().kind != tokRParen {
				return nil, fmt.Errorf("expected ')' in call to %s at position %d", tok.val, p.peek().pos)
			}
			p.advance()
			return callExpr{name: tok.val, args: args}, nil
		}
		switch strings.ToLower(tok.val) {
		case "true":
			return literalExpr{val: true}, nil
		case "false":
			return literalExpr{val: false}, nil
		case "null":
			return literalExpr{val: nil}, nil
		default:
			return identExpr{name: tok.val}, nil
		}

	default:
		return nil, fmt.Errorf("unexpected token %q at position %d", tok.val, tok.pos)
	}
}

// astContainsStatusFunc reports whether node contains a call to any GitHub Actions
// job status check function (success, always, failure, cancelled).
// String literals such as 'always()' are never treated as status function calls.
func astContainsStatusFunc(node exprNode) bool {
	if node == nil {
		return false
	}
	switch n := node.(type) {
	case callExpr:
		switch strings.ToLower(n.name) {
		case "success", "always", "failure", "cancelled":
			return true
		}
		for _, arg := range n.args {
			if astContainsStatusFunc(arg) {
				return true
			}
		}
		return false
	case binaryExpr:
		return astContainsStatusFunc(n.left) || astContainsStatusFunc(n.right)
	case unaryExpr:
		return astContainsStatusFunc(n.operand)
	case memberExpr:
		return astContainsStatusFunc(n.target)
	case indexExpr:
		return astContainsStatusFunc(n.target) || astContainsStatusFunc(n.index)
	default:
		return false
	}
}

// unwrapOuterExpression removes a single outer ${{ ... }} wrapper when the matching
// closing delimiter is at the end of s.
func unwrapOuterExpression(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") {
		return trimmed, false
	}
	endIdx := findMatchingExpressionEnd(trimmed[3:])
	if endIdx != -1 && endIdx == len(trimmed[3:])-2 {
		return strings.TrimSpace(trimmed[3 : len(trimmed)-2]), true
	}
	return trimmed, false
}

func containsStatusCheckFunction(s string) bool {
	unwrapped, _ := unwrapOuterExpression(s)
	if unwrapped == "" {
		return false
	}
	node, err := parseExpression(unwrapped)
	if err != nil {
		return false
	}
	return astContainsStatusFunc(node)
}

// shouldExecuteStep determines whether a job step should execute based on its condition.
func shouldExecuteStep(cond string, ctx map[string]any, jobFailed, jobCancelled bool) bool {
	trimmed := strings.TrimSpace(cond)
	if trimmed == "" {
		return !jobFailed && !jobCancelled
	}

	unwrapped, _ := unwrapOuterExpression(trimmed)
	if unwrapped == "" {
		return !jobFailed && !jobCancelled
	}

	node, err := parseExpression(unwrapped)
	if err != nil {
		return false
	}

	// When the condition calls none of success(), always(), failure(), or cancelled(),
	// GitHub Actions implicitly requires !jobFailed && !jobCancelled.
	if !astContainsStatusFunc(node) {
		if jobFailed || jobCancelled {
			return false
		}
	}

	evalCtx := make(map[string]any, len(ctx)+4)
	for k, v := range ctx {
		evalCtx[k] = v
	}
	evalCtx["_job_failed"] = jobFailed
	evalCtx["_job_cancelled"] = jobCancelled

	res := evalNode(node, evalCtx)
	return isTruthyVal(res)
}

func evaluateExpression(expr string, ctx map[string]any) string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return ""
	}

	if unwrapped, ok := unwrapOuterExpression(trimmed); ok {
		return evalPureExpression(unwrapped, ctx, false)
	}

	// If trimmed parses cleanly as a single AST (e.g. a format(...) call or 'literal'
	// that happens to contain ${{ inside quotes), evaluate it directly.
	if node, err := parseExpression(trimmed); err == nil {
		return valueToString(evalNode(node, ctx))
	}

	// Interpolate embedded ${{ ... }} expressions within surrounding text.
	if strings.Contains(trimmed, "${{") && strings.Contains(trimmed, "}}") {
		var sb strings.Builder
		remaining := trimmed
		hasEmbedded := false
		for {
			s := strings.Index(remaining, "${{")
			if s == -1 {
				sb.WriteString(remaining)
				break
			}
			eRel := findMatchingExpressionEnd(remaining[s+len("${{"):])
			if eRel == -1 {
				sb.WriteString(remaining)
				break
			}
			hasEmbedded = true
			e := s + len("${{") + eRel
			sb.WriteString(remaining[:s])
			inner := strings.TrimSpace(remaining[s+len("${{") : e])
			sb.WriteString(evalPureExpression(inner, ctx, false))
			remaining = remaining[e+len("}}"):]
		}
		if hasEmbedded {
			return sb.String()
		}
	}

	return trimmed
}

func evalPureExpression(expr string, ctx map[string]any, fallbackRaw bool) string {
	if expr == "" {
		return ""
	}
	node, err := parseExpression(expr)
	if err != nil {
		if fallbackRaw {
			return expr
		}
		return ""
	}
	return valueToString(evalNode(node, ctx))
}

func evalNode(node exprNode, ctx map[string]any) any {
	switch n := node.(type) {
	case literalExpr:
		return n.val

	case identExpr:
		if val, ok := lookupKey(ctx, n.name); ok {
			return val
		}
		return nil

	case memberExpr:
		target := evalNode(n.target, ctx)
		return accessProperty(target, n.prop)

	case indexExpr:
		target := evalNode(n.target, ctx)
		idx := evalNode(n.index, ctx)
		return accessIndex(target, idx)

	case unaryExpr:
		if n.op == "!" {
			return !isTruthyVal(evalNode(n.operand, ctx))
		}
		return nil

	case binaryExpr:
		switch n.op {
		case "||":
			left := evalNode(n.left, ctx)
			if isTruthyVal(left) {
				return left
			}
			return evalNode(n.right, ctx)
		case "&&":
			left := evalNode(n.left, ctx)
			if !isTruthyVal(left) {
				return left
			}
			return evalNode(n.right, ctx)
		case "==":
			return valuesEqual(evalNode(n.left, ctx), evalNode(n.right, ctx))
		case "!=":
			return !valuesEqual(evalNode(n.left, ctx), evalNode(n.right, ctx))
		case "<", "<=", ">", ">=":
			return compareValues(n.op, evalNode(n.left, ctx), evalNode(n.right, ctx))
		}
		return nil

	case callExpr:
		return evalCall(n, ctx)

	default:
		return nil
	}
}

func evalCall(n callExpr, ctx map[string]any) any {
	switch strings.ToLower(n.name) {
	case "success":
		failed, _ := ctx["_job_failed"].(bool)
		cancelled, _ := ctx["_job_cancelled"].(bool)
		return !failed && !cancelled
	case "always":
		return true
	case "failure":
		failed, _ := ctx["_job_failed"].(bool)
		cancelled, _ := ctx["_job_cancelled"].(bool)
		return failed && !cancelled
	case "cancelled":
		cancelled, _ := ctx["_job_cancelled"].(bool)
		return cancelled

	case "contains":
		if len(n.args) < 2 {
			return false
		}
		haystack := evalNode(n.args[0], ctx)
		needle := evalNode(n.args[1], ctx)
		return evalContains(haystack, needle)

	case "startswith":
		if len(n.args) < 2 {
			return false
		}
		s := strings.ToLower(valueToString(evalNode(n.args[0], ctx)))
		prefix := strings.ToLower(valueToString(evalNode(n.args[1], ctx)))
		return strings.HasPrefix(s, prefix)

	case "endswith":
		if len(n.args) < 2 {
			return false
		}
		s := strings.ToLower(valueToString(evalNode(n.args[0], ctx)))
		suffix := strings.ToLower(valueToString(evalNode(n.args[1], ctx)))
		return strings.HasSuffix(s, suffix)

	case "format":
		if len(n.args) == 0 {
			return ""
		}
		tmpl := valueToString(evalNode(n.args[0], ctx))
		argVals := make([]string, 0, len(n.args)-1)
		for _, arg := range n.args[1:] {
			argVals = append(argVals, valueToString(evalNode(arg, ctx)))
		}
		return formatTemplate(tmpl, argVals)

	case "join":
		if len(n.args) == 0 {
			return ""
		}
		val := evalNode(n.args[0], ctx)
		sep := ","
		if len(n.args) >= 2 {
			sep = valueToString(evalNode(n.args[1], ctx))
		}
		return evalJoin(val, sep)

	case "tojson":
		if len(n.args) == 0 {
			return "null"
		}
		val := unwrapValue(evalNode(n.args[0], ctx))
		data, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return "null"
		}
		return string(data)

	case "fromjson":
		if len(n.args) == 0 {
			return nil
		}
		raw := valueToString(evalNode(n.args[0], ctx))
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		var out any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil
		}
		return out

	case "hashfiles":
		patterns := make([]string, 0, len(n.args))
		for _, arg := range n.args {
			patterns = append(patterns, valueToString(evalNode(arg, ctx)))
		}
		return evalHashFiles(patterns, ctx)

	case "eq":
		if len(n.args) < 2 {
			return false
		}
		return valuesEqual(evalNode(n.args[0], ctx), evalNode(n.args[1], ctx))

	case "ne":
		if len(n.args) < 2 {
			return false
		}
		return !valuesEqual(evalNode(n.args[0], ctx), evalNode(n.args[1], ctx))

	case "and":
		for _, arg := range n.args {
			if !isTruthyVal(evalNode(arg, ctx)) {
				return false
			}
		}
		return true

	case "or":
		for _, arg := range n.args {
			if isTruthyVal(evalNode(arg, ctx)) {
				return true
			}
		}
		return false

	case "not":
		if len(n.args) == 0 {
			return true
		}
		return !isTruthyVal(evalNode(n.args[0], ctx))

	default:
		return nil
	}
}

func evalContains(haystack, needle any) bool {
	switch arr := haystack.(type) {
	case []any:
		for _, item := range arr {
			if valuesEqual(item, needle) {
				return true
			}
		}
		return false
	case []string:
		for _, item := range arr {
			if valuesEqual(item, needle) {
				return true
			}
		}
		return false
	default:
		s := strings.ToLower(valueToString(haystack))
		sub := strings.ToLower(valueToString(needle))
		return strings.Contains(s, sub)
	}
}

func evalJoin(val any, sep string) string {
	switch arr := val.(type) {
	case []any:
		parts := make([]string, len(arr))
		for i, item := range arr {
			parts[i] = valueToString(item)
		}
		return strings.Join(parts, sep)
	case []string:
		return strings.Join(arr, sep)
	case string:
		return arr
	default:
		return ""
	}
}

func evalHashFiles(patterns []string, ctx map[string]any) string {
	if len(patterns) == 0 {
		return ""
	}

	workspace := "."
	if wsVal := accessProperty(lookupRoot(ctx, "github"), "workspace"); wsVal != nil {
		if ws := strings.TrimSpace(valueToString(wsVal)); ws != "" {
			workspace = ws
		}
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return ""
	}
	evalWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		evalWorkspace = absWorkspace
	}

	var includes []string
	var excludes []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "!") {
			excludes = append(excludes, strings.TrimPrefix(p, "!"))
		} else {
			includes = append(includes, p)
		}
	}
	if len(includes) == 0 {
		return ""
	}

	matchedSet := make(map[string]struct{})
	_ = filepath.WalkDir(absWorkspace, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !hasPathPrefix(path, absWorkspace) {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !hasPathPrefix(target, evalWorkspace) {
				return nil
			}
			if info, statErr := os.Stat(target); statErr != nil || info.IsDir() {
				return nil
			}
		}
		rel, err := filepath.Rel(absWorkspace, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		included := false
		for _, pat := range includes {
			if matchGlob(filepath.ToSlash(pat), relSlash) {
				included = true
				break
			}
		}
		if !included {
			return nil
		}
		for _, pat := range excludes {
			if matchGlob(filepath.ToSlash(pat), relSlash) {
				return nil
			}
		}
		matchedSet[path] = struct{}{}
		return nil
	})

	if len(matchedSet) == 0 {
		return ""
	}

	paths := make([]string, 0, len(matchedSet))
	for p := range matchedSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	combined := sha256.New()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		fileHash := sha256.New()
		_, copyErr := io.Copy(fileHash, f)
		_ = f.Close()
		if copyErr != nil {
			continue
		}
		combined.Write(fileHash.Sum(nil))
	}
	return hex.EncodeToString(combined.Sum(nil))
}

func matchGlob(pattern, target string) bool {
	patSegs := strings.Split(strings.TrimPrefix(pattern, "./"), "/")
	tgtSegs := strings.Split(strings.TrimPrefix(target, "./"), "/")
	return matchSegments(patSegs, tgtSegs)
}

func matchSegments(pat, tgt []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(tgt); i++ {
				if matchSegments(pat[1:], tgt[i:]) {
					return true
				}
			}
			return false
		}
		if len(tgt) == 0 {
			return false
		}
		matched, err := filepath.Match(pat[0], tgt[0])
		if err != nil || !matched {
			return false
		}
		pat = pat[1:]
		tgt = tgt[1:]
	}
	return len(tgt) == 0
}

func lookupRoot(ctx map[string]any, key string) any {
	val, _ := lookupKey(ctx, key)
	return val
}

func lookupKey(m map[string]any, key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	if val, ok := m[key]; ok {
		return val, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

func lookupStringKey(m map[string]string, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	if val, ok := m[key]; ok {
		return val, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func accessProperty(target any, prop string) any {
	if target == nil {
		return nil
	}
	if prop == "*" {
		switch v := target.(type) {
		case []any:
			return v
		case []string:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = item
			}
			return out
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make([]any, 0, len(keys))
			for _, k := range keys {
				out = append(out, v[k])
			}
			return out
		case map[string]string:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out := make([]any, 0, len(keys))
			for _, k := range keys {
				out = append(out, v[k])
			}
			return out
		}
		return nil
	}

	switch m := target.(type) {
	case map[string]any:
		val, _ := lookupKey(m, prop)
		return val
	case map[string]string:
		if val, ok := lookupStringKey(m, prop); ok {
			return val
		}
		return nil
	default:
		return nil
	}
}

func accessIndex(target, idx any) any {
	if target == nil {
		return nil
	}
	switch t := target.(type) {
	case map[string]any:
		val, _ := lookupKey(t, valueToString(idx))
		return val
	case map[string]string:
		if val, ok := lookupStringKey(t, valueToString(idx)); ok {
			return val
		}
		return nil
	case []any:
		n, ok := toNumber(idx)
		if !ok || math.Trunc(n) != n {
			return nil
		}
		i := int(n)
		if i < 0 || i >= len(t) {
			return nil
		}
		return t[i]
	case []string:
		n, ok := toNumber(idx)
		if !ok || math.Trunc(n) != n {
			return nil
		}
		i := int(n)
		if i < 0 || i >= len(t) {
			return nil
		}
		return t[i]
	default:
		return nil
	}
}

func unwrapValue(v any) any {
	if num, ok := v.(exprNumber); ok {
		return num.val
	}
	return v
}

func valueToString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case exprNumber:
		return val.raw
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(val) {
			return "NaN"
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	default:
		return fmt.Sprint(val)
	}
}

func isTruthyVal(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case exprNumber:
		return val.val != 0 && !math.IsNaN(val.val)
	case float64:
		return val != 0 && !math.IsNaN(val)
	case int:
		return val != 0
	case int64:
		return val != 0
	case string:
		return val != ""
	default:
		return true
	}
}

func toNumber(v any) (float64, bool) {
	if v == nil {
		return 0, true
	}
	switch val := v.(type) {
	case bool:
		if val {
			return 1, true
		}
		return 0, true
	case exprNumber:
		if math.IsNaN(val.val) {
			return 0, false
		}
		return val.val, true
	case float64:
		if math.IsNaN(val) {
			return 0, false
		}
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return 0, true
		}
		if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") ||
			strings.HasPrefix(trimmed, "-0x") || strings.HasPrefix(trimmed, "-0X") {
			if n, err := strconv.ParseInt(trimmed, 0, 64); err == nil {
				return float64(n), true
			}
			return 0, false
		}
		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(f) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func valuesEqual(a, b any) bool {
	a = unwrapValue(a)
	b = unwrapValue(b)

	if a == nil && b == nil {
		return true
	}
	if ba, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			return ba == bb
		}
	}
	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok {
			return strings.EqualFold(sa, sb)
		}
	}

	na, okA := toNumber(a)
	nb, okB := toNumber(b)
	if !okA || !okB {
		return false
	}
	return na == nb
}

func compareValues(op string, a, b any) bool {
	a = unwrapValue(a)
	b = unwrapValue(b)

	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok {
			cmp := strings.Compare(strings.ToLower(sa), strings.ToLower(sb))
			switch op {
			case "<":
				return cmp < 0
			case "<=":
				return cmp <= 0
			case ">":
				return cmp > 0
			case ">=":
				return cmp >= 0
			}
			return false
		}
	}

	na, okA := toNumber(a)
	nb, okB := toNumber(b)
	if !okA || !okB {
		return false
	}
	switch op {
	case "<":
		return na < nb
	case "<=":
		return na <= nb
	case ">":
		return na > nb
	case ">=":
		return na >= nb
	}
	return false
}

func formatTemplate(templateStr string, argVals []string) string {
	var sb strings.Builder
	for i := 0; i < len(templateStr); {
		if templateStr[i] == '{' {
			if i+1 < len(templateStr) && templateStr[i+1] == '{' {
				sb.WriteByte('{')
				i += 2
				continue
			}
			closeIdx := strings.IndexByte(templateStr[i+1:], '}')
			if closeIdx != -1 {
				numStr := templateStr[i+1 : i+1+closeIdx]
				if idx, err := strconv.Atoi(numStr); err == nil && idx >= 0 && idx < len(argVals) {
					sb.WriteString(argVals[idx])
					i += 1 + closeIdx + 1
					continue
				}
			}
			sb.WriteByte('{')
			i++
			continue
		}
		if templateStr[i] == '}' {
			if i+1 < len(templateStr) && templateStr[i+1] == '}' {
				sb.WriteByte('}')
				i += 2
				continue
			}
			sb.WriteByte('}')
			i++
			continue
		}
		sb.WriteByte(templateStr[i])
		i++
	}
	return sb.String()
}

func findMatchingExpressionEnd(s string) int {
	inQuotes := false
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '\'' {
			if inQuotes && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && s[i] == '}' && s[i+1] == '}' {
			return i
		}
	}
	return -1
}
