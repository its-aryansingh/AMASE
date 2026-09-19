package audit

import "strings"

// IsReadOnly decides, from the text alone, whether a command can change state.
//
// This is the function that exists to replace a language-model call. goose's
// SmartApprove mode, when it meets a tool it has no stored permission for, sends
// the tool name and arguments to the configured provider and asks it to classify
// the call as read-only or not (crates/goose/src/permission/permission_judge.rs).
// That costs a round trip and tokens on the critical path, returns a different
// answer on different days for the same input, and -- the part that matters --
// takes its input from arguments the agent composed after reading files whose
// contents an attacker may control.
//
// The contract here is the opposite in every respect: no network, no tokens,
// microseconds, identical answer for identical input, and a bias that is
// explicitly one-directional. Unknown is never read-only. Being wrong in the
// safe direction costs an approval prompt. Being wrong in the unsafe direction
// costs whatever the command did.
func IsReadOnly(s *Script, ctx *Context) bool {
	if s == nil || s.Partial || len(s.Commands) == 0 {
		return false
	}

	extra := map[string]bool{}
	if ctx != nil {
		for _, c := range ctx.ExtraReadOnly {
			extra[c] = true
		}
	}

	for _, c := range s.Commands {
		// Any assignment prefix can change how the child process behaves --
		// LD_PRELOAD being the obvious one -- so a command carrying one is not
		// classified from its name.
		if len(c.Assignments) > 0 && len(c.Words) > 0 {
			return false
		}
		if len(c.Words) == 0 {
			if len(c.Redirects) > 0 {
				return false // `> file` with no command still truncates it
			}
			continue
		}
		// Not just argv[0]: `cat $(echo go.mod)` has a knowable program and an
		// unknowable argument, and an unknowable argument can be `-i`, a path
		// outside the workspace, or a second command's worth of text.
		for _, w := range c.Words {
			if !w.Resolvable() {
				return false
			}
		}
		for _, r := range c.Redirects {
			if r.Writes() {
				return false
			}
		}

		base := c.Base()
		if !readOnlyCommands[base] && !extra[base] {
			if !readOnlySubcommand(c) {
				return false
			}
			continue
		}
		if !readOnlyFlags(c) {
			return false
		}
	}
	return true
}

// readOnlyFlags catches the commands on the allow list that have a mode which
// writes. These exceptions are the whole reason a name-based list is not enough:
// `find` is read-only until -delete, `sed` until -i, `sort` until -o.
func readOnlyFlags(c Command) bool {
	switch c.Base() {
	case "find":
		for _, a := range c.Args() {
			if a == "-delete" || a == "-exec" || a == "-execdir" || a == "-ok" || a == "-fprint" {
				return false
			}
		}
	case "sed":
		if c.HasFlag("-i", "--in-place") {
			return false
		}
		for _, a := range c.Args() {
			if strings.HasPrefix(a, "-i") {
				return false
			}
		}
	case "sort":
		if c.HasFlag("-o") {
			return false
		}
	case "tr", "cut", "column":
		// pure filters
	case "env", "printenv":
		// `env FOO=bar cmd` runs cmd; only the bare dump is read-only.
		if len(c.Args()) > 0 {
			return false
		}
	}
	return true
}

// readOnlySubcommand handles tools whose safety depends on the verb rather than
// the binary. These are the cases a person would get right and a name list gets
// wrong: `git log` and `git push --force` are the same program.
func readOnlySubcommand(c Command) bool {
	verb := ""
	for _, a := range c.Args() {
		if !strings.HasPrefix(a, "-") {
			verb = a
			break
		}
	}
	if verb == "" {
		return false
	}

	switch c.Base() {
	case "git":
		switch verb {
		case "status", "log", "diff", "show", "branch", "remote", "config",
			"describe", "blame", "shortlog", "ls-files", "ls-remote", "rev-parse",
			"cat-file", "grep", "tag", "stash":
			// `git config key value` writes; the read form has at most one operand.
			if verb == "config" && len(c.Operands()) > 2 {
				return false
			}
			if verb == "branch" && (c.HasFlag("-d", "-D", "-m", "-M")) {
				return false
			}
			if verb == "tag" && len(c.Operands()) > 1 {
				return false
			}
			if verb == "stash" && len(c.Operands()) > 1 {
				return false
			}
			return true
		}
	case "kubectl", "oc":
		switch verb {
		case "get", "describe", "logs", "explain", "api-resources", "api-versions",
			"version", "config", "top", "cluster-info", "auth":
			if verb == "config" && (containsArg(c, "set") || containsArg(c, "use-context")) {
				return false
			}
			return true
		}
	case "docker", "podman":
		switch verb {
		case "ps", "images", "logs", "inspect", "version", "info", "top", "port", "diff":
			return true
		}
	case "npm", "pnpm", "yarn":
		switch verb {
		case "ls", "list", "view", "info", "outdated", "why", "audit":
			return c.Base() != "npm" || !containsArg(c, "fix")
		}
	case "pip", "pip3":
		switch verb {
		case "list", "show", "freeze", "check":
			return true
		}
	case "go":
		switch verb {
		case "version", "env", "list", "doc", "vet":
			// `go env -w` writes the environment file.
			return !c.HasFlag("-w")
		}
	case "cargo":
		switch verb {
		case "tree", "metadata", "search":
			return true
		}
	case "terraform":
		switch verb {
		case "plan", "show", "output", "validate", "fmt":
			return verb != "fmt" || c.HasFlag("-check", "--check")
		}
	case "systemctl":
		switch verb {
		case "status", "list-units", "list-unit-files", "show", "is-active", "is-enabled":
			return true
		}
	case "aws", "gcloud", "az":
		for _, a := range c.Args() {
			switch a {
			case "list", "describe", "get", "ls", "show":
				return true
			}
		}
	}
	return false
}
