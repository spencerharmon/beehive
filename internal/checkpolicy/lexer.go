package checkpolicy

import (
	"fmt"
	"strings"
)

// commandWords statically extracts the COMMAND-POSITION words of a POSIX-ish shell
// string — the binaries the check actually invokes (the first word of the pipeline
// stage / list element, after env-assignment prefixes and shell keywords), across
// pipes, `;`, `&&`/`||`, subshells, and command substitutions (recursed). It is a
// verifier, not a full shell: any construct it cannot resolve to a concrete command
// word — a variable used as the command (`$CMD …`), a command substitution in
// command position (`$(pick) …`), an `eval` — is returned as an error so the caller
// fails CLOSED (an un-vetted command never passes the allowlist by silence).
func commandWords(s string) ([]string, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	var words []string
	atCmd := true // start of input is a command position
	skipRedirectTarget := false
	headerSkip := false // for/select/case header: loop vars/list words, not commands
	for _, t := range toks {
		switch t.kind {
		case tPipe, tAnd, tOr, tSemi, tAmp, tNewline, tLparen, tLbrace:
			atCmd = true
			skipRedirectTarget = false
			headerSkip = false
		case tRparen, tRbrace:
			atCmd = false
			skipRedirectTarget = false
			headerSkip = false
		case tRedirect:
			skipRedirectTarget = true // the NEXT word is a filename, not a command
		case tCmdSubst:
			if atCmd {
				return nil, fmt.Errorf("a command substitution in command position (%q) cannot be statically verified", oneLine(t.val))
			}
			inner, ierr := commandWords(t.val)
			if ierr != nil {
				return nil, ierr
			}
			words = append(words, inner...)
		case tWord:
			if skipRedirectTarget {
				skipRedirectTarget = false
				continue
			}
			if headerSkip {
				if t.val == "do" { // `for … in …; do` may omit the `;` before do
					headerSkip = false
				}
				continue // loop variable / iteration list word, not a command
			}
			if !atCmd {
				// argument position: still recurse any command substitution embedded in
				// the argument (lex surfaces those as separate tCmdSubst tokens, so a
				// bare-word arg here has none) — nothing to validate.
				continue
			}
			if isAssignment(t.val) {
				continue // VAR=val prefix; still at command position
			}
			if isKeyword(t.val) {
				if t.val == "for" || t.val == "select" || t.val == "case" {
					headerSkip = true // skip loop var + iteration list until `do`/`;`/newline
				}
				continue // if/then/while/…: the command follows; stay at command position
			}
			if strings.ContainsAny(t.val, "$`") {
				return nil, fmt.Errorf("a variable/expansion used as a command (%q) cannot be statically verified", oneLine(t.val))
			}
			words = append(words, t.val)
			atCmd = false
		}
	}
	return words, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := w[j]
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (j > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "select": true,
	"!": true, "time": true, "function": true, "{": true, "}": true,
}

func isKeyword(w string) bool { return shellKeywords[w] }

type tokKind int

const (
	tWord tokKind = iota
	tPipe
	tAnd
	tOr
	tSemi
	tAmp
	tNewline
	tLparen
	tRparen
	tLbrace
	tRbrace
	tRedirect
	tCmdSubst // val holds the inner command string
)

type token struct {
	kind tokKind
	val  string
}

// lex tokenizes a shell string enough to find command positions: it honors single
// and double quotes, backslash escapes, `$(…)` and backtick command substitution
// (captured as a tCmdSubst whose val is the inner text), and the control/redirect
// operators. It is deliberately small; an unterminated quote or substitution is an
// error (fail closed).
func lex(s string) ([]token, error) {
	var toks []token
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			toks = append(toks, token{tWord, word.String()})
			word.Reset()
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '\'':
			// single-quoted: literal until next '
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("unterminated single quote")
			}
			word.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case '"':
			// double-quoted: honor \" and \$ escapes and nested $( ) / backticks
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					word.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '$' && i+1 < len(s) && s[i+1] == '(' {
					inner, ni, err := scanParen(s, i+2)
					if err != nil {
						return nil, err
					}
					flush()
					toks = append(toks, token{tCmdSubst, inner})
					i = ni
					continue
				}
				if s[i] == '`' {
					inner, ni, err := scanBacktick(s, i+1)
					if err != nil {
						return nil, err
					}
					flush()
					toks = append(toks, token{tCmdSubst, inner})
					i = ni
					continue
				}
				word.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++ // closing "
		case '\\':
			if i+1 < len(s) {
				word.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		case '$':
			if i+1 < len(s) && s[i+1] == '(' {
				inner, ni, err := scanParen(s, i+2)
				if err != nil {
					return nil, err
				}
				flush()
				toks = append(toks, token{tCmdSubst, inner})
				i = ni
				continue
			}
			word.WriteByte(c) // a normal $VAR: keep in the word so command-position detection flags it
			i++
		case '`':
			inner, ni, err := scanBacktick(s, i+1)
			if err != nil {
				return nil, err
			}
			flush()
			toks = append(toks, token{tCmdSubst, inner})
			i = ni
		case ' ', '\t':
			flush()
			i++
		case '\n', '\r':
			flush()
			toks = append(toks, token{tNewline, "\n"})
			i++
		case '|':
			flush()
			if i+1 < len(s) && s[i+1] == '|' {
				toks = append(toks, token{tOr, "||"})
				i += 2
			} else {
				toks = append(toks, token{tPipe, "|"})
				i++
			}
		case '&':
			flush()
			if i+1 < len(s) && s[i+1] == '&' {
				toks = append(toks, token{tAnd, "&&"})
				i += 2
			} else if i+1 < len(s) && s[i+1] == '>' {
				toks = append(toks, token{tRedirect, "&>"})
				i += 2
			} else {
				toks = append(toks, token{tAmp, "&"})
				i++
			}
		case ';':
			flush()
			// `;;` (case terminator) collapses to a single list separator for our purpose
			for i < len(s) && s[i] == ';' {
				i++
			}
			toks = append(toks, token{tSemi, ";"})
		case '(':
			flush()
			toks = append(toks, token{tLparen, "("})
			i++
		case ')':
			flush()
			toks = append(toks, token{tRparen, ")"})
			i++
		case '{':
			flush()
			toks = append(toks, token{tLbrace, "{"})
			i++
		case '}':
			flush()
			toks = append(toks, token{tRbrace, "}"})
			i++
		case '>', '<':
			flush()
			op := string(c)
			i++
			for i < len(s) && (s[i] == '>' || s[i] == '<' || s[i] == '&') {
				op += string(s[i])
				i++
			}
			toks = append(toks, token{tRedirect, op})
		default:
			// a redirection with a leading fd number, e.g. `2>`
			word.WriteByte(c)
			i++
		}
	}
	flush()
	// Post-pass: fold a fd-number word immediately preceding a redirect (e.g. `2` `>`)
	// so it is not mistaken for a command word. lex emits `2` as a word then `>` as a
	// redirect; drop a trailing all-digit word when the next token is a redirect.
	return foldFdRedirects(toks), nil
}

func foldFdRedirects(toks []token) []token {
	out := make([]token, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		if toks[i].kind == tWord && i+1 < len(toks) && toks[i+1].kind == tRedirect && allDigits(toks[i].val) {
			continue // fd prefix of a redirect; drop it
		}
		out = append(out, toks[i])
	}
	return out
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// scanParen returns the text inside a `$(` … `)` starting at index `start` (just
// after the `(`), balancing nested parentheses, plus the index just past the `)`.
func scanParen(s string, start int) (string, int, error) {
	depth := 1
	i := start
	for i < len(s) {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[start:i], i + 1, nil
			}
		case '\'':
			if j := strings.IndexByte(s[i+1:], '\''); j >= 0 {
				i += j + 1
			}
		}
		i++
	}
	return "", 0, fmt.Errorf("unterminated command substitution $(")
}

// scanBacktick returns the text inside a backtick pair starting at `start` (just
// after the opening backtick), plus the index just past the closing backtick.
func scanBacktick(s string, start int) (string, int, error) {
	for i := start; i < len(s); i++ {
		if s[i] == '`' {
			return s[start:i], i + 1, nil
		}
	}
	return "", 0, fmt.Errorf("unterminated command substitution `")
}
