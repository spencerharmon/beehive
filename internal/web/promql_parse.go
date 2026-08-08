package web

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// PromQL subset lexer + recursive-descent parser.
//
// Supported grammar (sufficient for the beehive health/progress dashboards):
//   expr        := compare
//   compare     := addsub ( (==|!=|>|<|>=|<=) [bool] addsub )?      // non-chaining
//   addsub      := muldiv ( (+|-) muldiv )*
//   muldiv      := unary ( (*|/|%) unary )*
//   unary       := '-' unary | primary
//   primary     := NUMBER | '(' expr ')' | aggregation | call | selector
//   aggregation := AGG ( '(' [param ','] expr ')' [ (by|without) '(' labels ')' ]
//                       | (by|without) '(' labels ')' '(' [param ','] expr ')' )
//   call        := FUNC '(' expr ')'                                // rate/increase/delta/scalar
//   selector    := [IDENT] [ '{' matchers '}' ] [ '[' DURATION ']' ]
//   matcher     := IDENT (=|!=|=~|!~) STRING
// ============================================================================

type nodeKind int

const (
	nNum nodeKind = iota
	nSelector
	nRange
	nCall
	nAgg
	nBinary
	nUnary
)

type matcher struct {
	name string
	op   string // = != =~ !~
	val  string
	re   *regexp.Regexp // compiled for =~ / !~
}

func (m matcher) matches(v string) bool {
	switch m.op {
	case "=":
		return v == m.val
	case "!=":
		return v != m.val
	case "=~":
		return m.re != nil && m.re.MatchString(v)
	case "!~":
		return m.re != nil && !m.re.MatchString(v)
	}
	return false
}

type node struct {
	kind nodeKind
	// nNum
	num float64
	// nSelector
	name     string
	matchers []matcher
	// nRange
	dur time.Duration
	// nCall
	fn   string
	args []*node
	// nAgg
	op       string
	without  bool
	grouping []string
	param    *node
	// nBinary / nUnary / nAgg child
	lhs, rhs *node
	boolMod  bool
}

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tNumber
	tDuration
	tIdent
	tString
	tOp     // arithmetic / comparison / matcher operator
	tLBrace // {
	tRBrace // }
	tLParen // (
	tRParen // )
	tLBrack // [
	tRBrack // ]
	tComma
)

type tok struct {
	kind tokKind
	text string
}

var durationRE = regexp.MustCompile(`^([0-9]+(ms|s|m|h|d|w))+$`)

func lex(s string) ([]tok, error) {
	var toks []tok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '{':
			toks = append(toks, tok{tLBrace, "{"})
			i++
		case c == '}':
			toks = append(toks, tok{tRBrace, "}"})
			i++
		case c == '(':
			toks = append(toks, tok{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, tok{tRParen, ")"})
			i++
		case c == '[':
			toks = append(toks, tok{tLBrack, "["})
			i++
		case c == ']':
			toks = append(toks, tok{tRBrack, "]"})
			i++
		case c == ',':
			toks = append(toks, tok{tComma, ","})
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != c {
				if s[j] == '\\' && j+1 < len(s) {
					j++
					switch s[j] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(s[j])
					}
					j++
					continue
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, evalError{"unterminated string literal"}
			}
			toks = append(toks, tok{tString, b.String()})
			i = j + 1
		case c == '=' || c == '!' || c == '<' || c == '>':
			// two-char operators: == != <= >= =~ !~
			if i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '~') {
				toks = append(toks, tok{tOp, s[i : i+2]})
				i += 2
			} else {
				toks = append(toks, tok{tOp, string(c)})
				i++
			}
		case c == '+' || c == '-' || c == '*' || c == '/' || c == '%':
			toks = append(toks, tok{tOp, string(c)})
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
				j++
			}
			// A trailing unit run (ms/s/m/h/d/w) makes it a duration.
			k := j
			for k < len(s) && (s[k] >= 'a' && s[k] <= 'z') {
				k++
			}
			if k > j && durationRE.MatchString(s[i:k]) {
				toks = append(toks, tok{tDuration, s[i:k]})
				i = k
			} else {
				toks = append(toks, tok{tNumber, s[i:j]})
				i = j
			}
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			toks = append(toks, tok{tIdent, s[i:j]})
			i = j
		default:
			return nil, evalError{"unexpected character in query: " + string(c)}
		}
	}
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c == ':' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// ---- parser ----

type parser struct {
	toks []tok
	pos  int
}

func (p *parser) peek() tok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return tok{tEOF, ""}
}
func (p *parser) next() tok {
	t := p.peek()
	p.pos++
	return t
}
func (p *parser) expect(k tokKind, what string) (tok, error) {
	t := p.next()
	if t.kind != k {
		return t, evalError{"expected " + what}
	}
	return t, nil
}

var aggOps = map[string]bool{
	"sum": true, "avg": true, "min": true, "max": true, "count": true,
	"topk": true, "bottomk": true, "group": true, "stddev": true,
}
var funcOps = map[string]bool{"rate": true, "increase": true, "delta": true, "scalar": true}

func (p *parser) parseExpr() (*node, error) { return p.parseCompare() }

func (p *parser) parseCompare() (*node, error) {
	lhs, err := p.parseAddSub()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tOp && isComparison(t.text) {
		p.next()
		boolMod := false
		if n := p.peek(); n.kind == tIdent && n.text == "bool" {
			p.next()
			boolMod = true
		}
		rhs, err := p.parseAddSub()
		if err != nil {
			return nil, err
		}
		return &node{kind: nBinary, op: t.text, lhs: lhs, rhs: rhs, boolMod: boolMod}, nil
	}
	return lhs, nil
}

func (p *parser) parseAddSub() (*node, error) {
	lhs, err := p.parseMulDiv()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tOp && (t.text == "+" || t.text == "-") {
			p.next()
			rhs, err := p.parseMulDiv()
			if err != nil {
				return nil, err
			}
			lhs = &node{kind: nBinary, op: t.text, lhs: lhs, rhs: rhs}
			continue
		}
		return lhs, nil
	}
}

func (p *parser) parseMulDiv() (*node, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tOp && (t.text == "*" || t.text == "/" || t.text == "%") {
			p.next()
			rhs, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			lhs = &node{kind: nBinary, op: t.text, lhs: lhs, rhs: rhs}
			continue
		}
		return lhs, nil
	}
}

func (p *parser) parseUnary() (*node, error) {
	t := p.peek()
	if t.kind == tOp && t.text == "-" {
		p.next()
		sub, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &node{kind: nUnary, lhs: sub}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (*node, error) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.next()
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, evalError{"bad number: " + t.text}
		}
		return &node{kind: nNum, num: f}, nil
	case tLParen:
		p.next()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	case tIdent:
		if aggOps[t.text] {
			return p.parseAgg()
		}
		if funcOps[t.text] && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tLParen {
			return p.parseCall()
		}
		return p.parseSelector()
	case tLBrace:
		return p.parseSelector()
	}
	return nil, evalError{"unexpected token in expression"}
}

func (p *parser) parseCall() (*node, error) {
	fn := p.next().text
	if _, err := p.expect(tLParen, "'('"); err != nil {
		return nil, err
	}
	arg, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen, "')'"); err != nil {
		return nil, err
	}
	return &node{kind: nCall, fn: fn, args: []*node{arg}}, nil
}

func (p *parser) parseGrouping() ([]string, error) {
	if _, err := p.expect(tLParen, "'(' after by/without"); err != nil {
		return nil, err
	}
	var labelsOut []string
	for {
		if p.peek().kind == tRParen {
			p.next()
			break
		}
		id, err := p.expect(tIdent, "label name")
		if err != nil {
			return nil, err
		}
		labelsOut = append(labelsOut, id.text)
		if p.peek().kind == tComma {
			p.next()
		}
	}
	return labelsOut, nil
}

func (p *parser) parseAgg() (*node, error) {
	op := p.next().text
	n := &node{kind: nAgg, op: op}
	// Optional leading grouping: AGG by(...) (expr)
	if t := p.peek(); t.kind == tIdent && (t.text == "by" || t.text == "without") {
		p.next()
		n.without = t.text == "without"
		g, err := p.parseGrouping()
		if err != nil {
			return nil, err
		}
		n.grouping = g
	}
	if _, err := p.expect(tLParen, "'(' after aggregation"); err != nil {
		return nil, err
	}
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == tComma {
		// AGG(param, expr) — topk/bottomk
		p.next()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		n.param = first
		n.lhs = expr
	} else {
		n.lhs = first
	}
	if _, err := p.expect(tRParen, "')'"); err != nil {
		return nil, err
	}
	// Optional trailing grouping: AGG(expr) by(...)
	if t := p.peek(); t.kind == tIdent && (t.text == "by" || t.text == "without") {
		p.next()
		n.without = t.text == "without"
		g, err := p.parseGrouping()
		if err != nil {
			return nil, err
		}
		n.grouping = g
	}
	return n, nil
}

func (p *parser) parseSelector() (*node, error) {
	n := &node{kind: nSelector}
	if p.peek().kind == tIdent {
		n.name = p.next().text
	}
	if p.peek().kind == tLBrace {
		p.next()
		for {
			if p.peek().kind == tRBrace {
				p.next()
				break
			}
			lname, err := p.expect(tIdent, "label name in matcher")
			if err != nil {
				return nil, err
			}
			opt := p.next()
			if opt.kind != tOp || (opt.text != "=" && opt.text != "!=" && opt.text != "=~" && opt.text != "!~") {
				return nil, evalError{"expected matcher operator (= != =~ !~)"}
			}
			val, err := p.expect(tString, "matcher value string")
			if err != nil {
				return nil, err
			}
			m := matcher{name: lname.text, op: opt.text, val: val.text}
			if opt.text == "=~" || opt.text == "!~" {
				re, err := regexp.Compile("^(?:" + val.text + ")$")
				if err != nil {
					return nil, evalError{"bad regex in matcher: " + val.text}
				}
				m.re = re
			}
			n.matchers = append(n.matchers, m)
			if p.peek().kind == tComma {
				p.next()
			}
		}
	}
	if n.name == "" && len(n.matchers) == 0 {
		return nil, evalError{"empty selector"}
	}
	// Optional range: metric[5m]
	if p.peek().kind == tLBrack {
		p.next()
		d, err := p.expect(tDuration, "duration in range selector")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRBrack, "']'"); err != nil {
			return nil, err
		}
		dur, err := parsePromDuration(d.text)
		if err != nil {
			return nil, err
		}
		return &node{kind: nRange, lhs: n, dur: dur}, nil
	}
	return n, nil
}

// parsePromDuration parses a Prometheus duration (e.g. 5m, 1h, 7d, 1w, 500ms,
// 1h30m) into a time.Duration.
func parsePromDuration(s string) (time.Duration, error) {
	var total time.Duration
	i := 0
	for i < len(s) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return 0, evalError{"bad duration: " + s}
		}
		nnum, _ := strconv.Atoi(s[i:j])
		// unit
		k := j
		for k < len(s) && s[k] >= 'a' && s[k] <= 'z' {
			k++
		}
		unit := s[j:k]
		var mult time.Duration
		switch unit {
		case "ms":
			mult = time.Millisecond
		case "s":
			mult = time.Second
		case "m":
			mult = time.Minute
		case "h":
			mult = time.Hour
		case "d":
			mult = 24 * time.Hour
		case "w":
			mult = 7 * 24 * time.Hour
		default:
			return 0, evalError{"bad duration unit: " + unit}
		}
		total += time.Duration(nnum) * mult
		i = k
	}
	return total, nil
}
