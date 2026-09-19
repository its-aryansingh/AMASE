package audit

import (
	"fmt"
	"path"
	"strings"
)

// interpreters are programs that execute their standard input or a -c argument.
// Membership here is what makes `| sh` different from `| grep`.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"python": true, "python2": true, "python3": true, "perl": true, "ruby": true,
	"node": true, "deno": true, "bun": true, "php": true, "lua": true,
	"osascript": true, "powershell": true, "pwsh": true, "cmd": true,
}

// fetchers retrieve remote content.
var fetchers = map[string]bool{
	"curl": true, "wget": true, "aria2c": true, "http": true, "httpie": true,
	"fetch": true, "scp": true, "rsync": true, "nc": true, "ncat": true, "socat": true,
}

// readOnlyCommands never modify state when invoked without the flags noted in
// the per-command exceptions below. The list is intentionally short: anything
// not on it is treated as state-changing, which is the safe default and the
// opposite of how a language-model classifier behaves when it is unsure.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"wc": true, "sort": true, "uniq": true, "cut": true, "tr": true, "column": true,
	"file": true, "stat": true, "du": true, "df": true, "tree": true, "realpath": true,
	"basename": true, "dirname": true, "pwd": true, "whoami": true, "id": true,
	"date": true, "uname": true, "hostname": true, "echo": true, "printf": true,
	"which": true, "type": true, "command": true, "env": true, "printenv": true,
	"ps": true, "top": true, "uptime": true, "free": true, "jq": true, "yq": true,
	"diff": true, "cmp": true, "md5sum": true, "sha256sum": true, "cksum": true,
	// find and sed appear here because their read-only-ness is decided by flags
	// rather than by name; readOnlyFlags below carries the exceptions.
	"find": true, "sed": true,
	"true": true, "false": true, "test": true, "seq": true, "sleep": true,
}

// Rules is the ordered rule table. Order affects nothing but report readability;
// severity decides the verdict.
var Rules []Rule

func init() {
	Rules = []Rule{
		rulePipeToInterpreter,
		ruleUnresolvableCommand,
		ruleRecursiveDelete,
		ruleCredentialAccess,
		rulePrivilegeEscalation,
		ruleHistoryTampering,
		ruleDestructiveGit,
		ruleDataEgress,
		rulePersistence,
		ruleObfuscatedPayload,
		ruleWorkspaceEscape,
		ruleClusterDestruction,
		ruleFetchThenExecute,
		ruleSecurityControlDisable,
		ruleBlockDeviceWrite,
		ruleForkBomb,
	}
}

// --------------------------------------------------------------------- TG001

var rulePipeToInterpreter = Rule{
	ID:       "TG001",
	Title:    "Remote content piped into an interpreter",
	Severity: SevCritical,
	Rationale: "A command whose standard input comes from the network and whose " +
		"program is a shell or language runtime executes code that was never " +
		"reviewed, cannot be reviewed before it runs, and may differ between " +
		"the moment it is inspected and the moment it is fetched.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for i, c := range s.Commands {
			if !c.PipeFrom || !interpreters[c.EffectiveBase()] {
				continue
			}
			// Find the producer: the nearest preceding command at the same
			// nesting depth whose stdout is piped onward.
			producer := ""
			for j := i - 1; j >= 0; j-- {
				p := s.Commands[j]
				if p.Depth == c.Depth && p.PipeTo {
					producer = p.EffectiveBase()
					break
				}
			}
			if producer == "" {
				continue
			}
			if !fetchers[producer] {
				// `cat script.sh | sh` is local and merely unusual; the remote
				// case is the dangerous one.
				continue
			}
			out = append(out, finding(self,
				fmt.Sprintf("%s output is piped into %s", producer, c.EffectiveBase()),
				c.Text(),
				"download to a file, read it, then run it as a separate reviewed step"))
		}
		return out
	},
}

// --------------------------------------------------------------------- TG002

var ruleUnresolvableCommand = Rule{
	ID:       "TG002",
	Title:    "Program name is not statically determinable",
	Severity: SevHigh,
	Rationale: "When argv[0] is produced by a command substitution or a variable, " +
		"no static analysis can say what will run. Treating that as safe is the " +
		"single easiest way to defeat a guardrail, so it is treated as unknown " +
		"and escalated.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			if len(c.Words) == 0 || c.Resolvable() {
				continue
			}
			out = append(out, finding(self,
				"the program to execute is built at runtime",
				c.Text(),
				"name the program literally so the action can be reviewed"))
		}
		return out
	},
}

// --------------------------------------------------------------------- TG003

var dangerousRoots = []string{"/", "/etc", "/usr", "/var", "/bin", "/sbin", "/lib",
	"/boot", "/dev", "/opt", "/home", "/root", "/System", "/Library", "/Applications"}

var ruleRecursiveDelete = Rule{
	ID:       "TG003",
	Title:    "Recursive deletion outside the workspace",
	Severity: SevCritical,
	Rationale: "Recursive force-delete is unrecoverable and its blast radius is set " +
		"entirely by a path argument that an agent assembled from context it may " +
		"have misread.",
	Check: func(self Rule, s *Script, ctx *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			base := c.Base()
			recursive := c.HasFlag("-r", "-R", "--recursive")
			switch base {
			case "rm":
				if !recursive {
					continue
				}
			case "shred", "srm":
			default:
				continue
			}
			for _, target := range c.Operands() {
				sev := SevCritical
				reason := ""
				switch {
				case target == "/" || target == "/*":
					reason = "targets the filesystem root"
				case isDangerousRoot(target):
					reason = fmt.Sprintf("targets the system path %s", target)
				case strings.Contains(target, subPlaceholder):
					reason = "target path is computed at runtime"
				case ctx.HomeDir != "" && (target == ctx.HomeDir || target == "~" || target == "~/"):
					reason = "targets the home directory"
				case ctx.Workspace != "" && escapesWorkspace(target, ctx.Workspace):
					reason = fmt.Sprintf("target %s is outside the workspace", target)
				default:
					// Recursive delete inside the workspace is ordinary work.
					sev = SevMedium
					reason = fmt.Sprintf("recursive delete of %s", target)
				}
				f := finding(self, reason, c.Text(),
					"delete a named path inside the workspace, or move it aside instead")
				f.sev = sev
				f.Severity = sev.String()
				out = append(out, f)
			}
			if len(c.Operands()) == 0 {
				out = append(out, finding(self,
					"recursive delete with no literal target", c.Text(), ""))
			}
		}
		return out
	},
}

func isDangerousRoot(p string) bool {
	clean := path.Clean(p)
	for _, d := range dangerousRoots {
		if clean == d {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------------- TG004

var credentialPaths = []string{
	".ssh/", "id_rsa", "id_ed25519", ".aws/credentials", ".aws/config",
	".kube/config", ".docker/config.json", ".netrc", ".npmrc", ".pypirc",
	".gnupg", "credentials.json", "service-account", ".env", ".git-credentials",
	"secrets.yaml", "secrets.yml", ".pgpass", "keychain",
}

var ruleCredentialAccess = Rule{
	ID:       "TG004",
	Title:    "Access to credential material",
	Severity: SevHigh,
	Rationale: "Reading a private key or cloud credential is not dangerous in itself, " +
		"but it is the first half of every exfiltration, and it is almost never " +
		"needed to complete a coding task.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			for _, w := range c.Words[min(1, len(c.Words)):] {
				lower := strings.ToLower(w.Literal)
				for _, p := range credentialPaths {
					if strings.Contains(lower, p) {
						out = append(out, finding(self,
							fmt.Sprintf("references credential path %q", w.Literal),
							c.Text(),
							"pass the specific value the task needs through the environment instead"))
						break
					}
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG005

var rulePrivilegeEscalation = Rule{
	ID:       "TG005",
	Title:    "Privilege escalation or permission widening",
	Severity: SevHigh,
	Rationale: "An agent working on a project does not need root. A command that asks " +
		"for it has either misdiagnosed a permission error or is doing something " +
		"outside the task.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			switch c.Base() {
			case "sudo", "su", "doas", "runas":
				out = append(out, finding(self,
					fmt.Sprintf("escalates privileges via %s", c.Base()), c.Text(),
					"run the command as the current user, or fix the ownership of the file it needs"))
			case "chmod":
				for _, a := range c.Args() {
					if a == "777" || a == "-R777" || strings.HasSuffix(a, "777") ||
						strings.Contains(a, "+s") || a == "a+rwx" {
						out = append(out, finding(self,
							fmt.Sprintf("widens permissions to %s", a), c.Text(),
							"grant the narrowest mode that makes the task work"))
					}
				}
			case "chown":
				for _, a := range c.Operands() {
					if strings.HasPrefix(a, "root") {
						out = append(out, finding(self,
							"changes ownership to root", c.Text(), ""))
					}
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG006

var ruleHistoryTampering = Rule{
	ID:       "TG006",
	Title:    "Audit or history tampering",
	Severity: SevCritical,
	Rationale: "Nothing in a legitimate coding task benefits from erasing the record " +
		"of what was run. This rule also covers toolgate's own journal, because a " +
		"guardrail that can be silently blinded is decorative.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			for _, a := range append(c.Args(), c.Assignments...) {
				low := strings.ToLower(a)
				switch {
				case strings.Contains(low, "histfile"),
					strings.Contains(low, ".bash_history"),
					strings.Contains(low, ".zsh_history"),
					strings.Contains(low, "toolgate/journal"),
					strings.Contains(low, ".toolgate"):
					out = append(out, finding(self,
						fmt.Sprintf("touches audit state via %q", a), c.Text(), ""))
				}
			}
			if c.Base() == "history" && c.HasFlag("-c") {
				out = append(out, finding(self,
					"clears shell history", c.Text(), ""))
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG007

var ruleDestructiveGit = Rule{
	ID:       "TG007",
	Title:    "Destructive version-control operation",
	Severity: SevHigh,
	Rationale: "These commands discard work that has no other copy. They are ordinary " +
		"operations for a person who knows what is in the working tree, and a " +
		"coin flip for an agent that does not.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			if c.Base() != "git" {
				continue
			}
			args := c.Args()
			if len(args) == 0 {
				continue
			}
			sub := ""
			for _, a := range args {
				if !strings.HasPrefix(a, "-") {
					sub = a
					break
				}
			}
			switch sub {
			case "push":
				if c.HasFlag("-f", "--force") && !c.HasFlag("--force-with-lease") {
					out = append(out, finding(self,
						"force-push can overwrite commits on the remote", c.Text(),
						"use --force-with-lease so a concurrent push is detected"))
				}
			case "reset":
				if c.HasFlag("--hard") {
					out = append(out, finding(self,
						"hard reset discards uncommitted changes", c.Text(),
						"stash first, or use --keep"))
				}
			case "clean":
				if c.HasFlag("-f", "-x", "-d") {
					out = append(out, finding(self,
						"clean removes untracked files permanently", c.Text(),
						"run with -n first and read the list"))
				}
			case "checkout", "switch", "restore":
				if c.HasFlag("-f", "--force", "--hard") {
					out = append(out, finding(self,
						"forced checkout discards local modifications", c.Text(), ""))
				}
			case "branch":
				if c.HasFlag("-D") {
					out = append(out, finding(self,
						"deletes a branch without a merge check", c.Text(),
						"use -d so an unmerged branch is refused"))
				}
			case "filter-branch", "filter-repo":
				out = append(out, finding(self,
					"rewrites history across the repository", c.Text(), ""))
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG008

var ruleDataEgress = Rule{
	ID:       "TG008",
	Title:    "Local data sent to the network",
	Severity: SevHigh,
	Rationale: "A file read followed by an upload is the shape of every exfiltration, " +
		"whether the agent intended it or was talked into it by text it read.",
	Check: func(self Rule, s *Script, ctx *Context) []Finding {
		if ctx.AllowNetwork {
			return nil
		}
		var out []Finding
		for i, c := range s.Commands {
			base := c.Base()

			// Shape 1: an uploader carrying a file reference.
			if fetchers[base] {
				for _, a := range c.Args() {
					if strings.HasPrefix(a, "@") || a == "--data-binary" || a == "-T" || a == "--upload-file" {
						out = append(out, finding(self,
							fmt.Sprintf("%s uploads local file content", base), c.Text(),
							"if this is a deliberate upload, enable network egress explicitly"))
						break
					}
				}
			}

			// Shape 2: something reads, and the pipeline ends at the network.
			if !c.PipeTo {
				continue
			}
			producerReads := readOnlyCommands[base] || base == "env" || base == "printenv" ||
				base == "tar" || base == "zip" || base == "base64"
			if !producerReads {
				continue
			}
			for j := i + 1; j < len(s.Commands); j++ {
				n := s.Commands[j]
				if n.Depth != c.Depth {
					continue
				}
				if fetchers[n.Base()] {
					out = append(out, finding(self,
						fmt.Sprintf("%s output is piped to %s", base, n.Base()), c.Text(), ""))
				}
				if !n.PipeTo {
					break
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG009

var persistenceTargets = []string{
	".bashrc", ".bash_profile", ".zshrc", ".profile", "authorized_keys",
	"/etc/cron", "crontab", "/etc/systemd", ".config/systemd", "launchagents",
	"/etc/sudoers", ".gitconfig", "/etc/hosts", ".git/hooks/",
}

var rulePersistence = Rule{
	ID:       "TG009",
	Title:    "Write to a startup or hook location",
	Severity: SevCritical,
	Rationale: "These paths cause code to run later, outside the session that wrote " +
		"them. A change here outlives the agent, the review, and often the " +
		"person's memory of having approved it.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		hit := func(c Command, target string) {
			low := strings.ToLower(target)
			for _, p := range persistenceTargets {
				if strings.Contains(low, p) {
					out = append(out, finding(self,
						fmt.Sprintf("writes to %q", target), c.Text(),
						"keep changes inside the workspace"))
					return
				}
			}
		}
		for _, c := range s.Commands {
			for _, r := range c.Redirects {
				if r.Writes() {
					hit(c, r.Target.Literal)
				}
			}
			switch c.Base() {
			case "tee", "cp", "mv", "install", "ln":
				for _, a := range c.Operands() {
					hit(c, a)
				}
			case "crontab":
				out = append(out, finding(self,
					"modifies scheduled jobs", c.Text(), ""))
			case "systemctl":
				if c.HasFlag("enable") || containsArg(c, "enable") {
					out = append(out, finding(self,
						"enables a service to start automatically", c.Text(), ""))
				}
			}
		}
		return out
	},
}

func containsArg(c Command, want string) bool {
	for _, a := range c.Args() {
		if a == want {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------------- TG010

var ruleObfuscatedPayload = Rule{
	ID:       "TG010",
	Title:    "Obfuscated or decoded payload execution",
	Severity: SevCritical,
	Rationale: "Decoding then executing removes the only opportunity anyone had to " +
		"read the code. There is no legitimate reason for an agent to hide what " +
		"it is about to run from the person who approved it.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for i, c := range s.Commands {
			base := c.Base()
			decoder := base == "base64" || base == "xxd" || base == "uudecode" ||
				(base == "openssl" && containsArg(c, "enc")) ||
				(base == "tr" && c.HasFlag("-d"))

			if decoder && c.PipeTo {
				for j := i + 1; j < len(s.Commands); j++ {
					n := s.Commands[j]
					if n.Depth != c.Depth {
						continue
					}
					if interpreters[n.EffectiveBase()] {
						out = append(out, finding(self,
							fmt.Sprintf("%s output is executed by %s", base, n.EffectiveBase()),
							c.Text(), "decode to a file and read it before running it"))
					}
					if !n.PipeTo {
						break
					}
				}
			}

			if base == "eval" || (interpreters[base] && c.HasFlag("-c")) {
				for _, w := range c.Words[min(1, len(c.Words)):] {
					if w.Flags.Has(FlagCommandSub) {
						out = append(out, finding(self,
							"evaluates a string built by another command", c.Text(), ""))
						break
					}
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG011

var ruleWorkspaceEscape = Rule{
	ID:       "TG011",
	Title:    "Write outside the workspace",
	Severity: SevHigh,
	Rationale: "The workspace is the blast radius the person agreed to. A write " +
		"outside it is, by definition, a change they did not scope.",
	Check: func(self Rule, s *Script, ctx *Context) []Finding {
		if ctx.Workspace == "" {
			return nil
		}
		var out []Finding
		for _, c := range s.Commands {
			for _, r := range c.Redirects {
				if r.Writes() && escapesWorkspace(r.Target.Literal, ctx.Workspace) {
					out = append(out, finding(self,
						fmt.Sprintf("redirects output to %q", r.Target.Literal), c.Text(), ""))
				}
			}
			switch c.Base() {
			case "cp", "mv", "tee", "install", "touch", "mkdir", "ln", "dd":
				for _, a := range c.Operands() {
					if escapesWorkspace(a, ctx.Workspace) {
						out = append(out, finding(self,
							fmt.Sprintf("writes to %q", a), c.Text(), ""))
					}
				}
			}
		}
		return out
	},
}

// escapesWorkspace reports whether a path certainly lands outside root.
//
// Relative paths without a traversal component are assumed inside, which is a
// deliberate limitation rather than an oversight: the analyser does not know the
// process's working directory. The Context is supplied by the caller that does,
// and `exec_guarded` resolves relative paths against it before calling in.
func escapesWorkspace(p, root string) bool {
	if p == "" || strings.Contains(p, subPlaceholder) {
		return false
	}
	if strings.HasPrefix(p, "~") {
		return true
	}
	if !strings.HasPrefix(p, "/") {
		if !strings.Contains(p, "..") {
			return false
		}
		p = path.Join(root, p)
	}
	clean := path.Clean(p)
	root = path.Clean(root)
	return clean != root && !strings.HasPrefix(clean, root+"/")
}

// --------------------------------------------------------------------- TG012

var ruleClusterDestruction = Rule{
	ID:       "TG012",
	Title:    "Destructive infrastructure operation",
	Severity: SevCritical,
	Rationale: "The gap between a development namespace and production is one flag, " +
		"and an agent cannot see which context is currently selected.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			switch c.Base() {
			case "kubectl", "oc":
				for _, verb := range []string{"delete", "drain", "cordon", "taint"} {
					if containsArg(c, verb) {
						detail := fmt.Sprintf("kubectl %s against the active context", verb)
						if c.HasFlag("--all") || containsArg(c, "--all-namespaces") || c.HasFlag("-A") {
							detail += " across all namespaces"
						}
						out = append(out, finding(self, detail, c.Text(),
							"name the context and namespace explicitly, and dry-run first"))
					}
				}
			case "terraform":
				if containsArg(c, "destroy") || (containsArg(c, "apply") && c.HasFlag("-auto-approve", "--auto-approve")) {
					out = append(out, finding(self,
						"applies infrastructure changes without a review gate", c.Text(), ""))
				}
			case "aws", "gcloud", "az":
				for _, verb := range []string{"delete", "rm", "destroy", "terminate-instances"} {
					if containsArg(c, verb) {
						out = append(out, finding(self,
							fmt.Sprintf("cloud resource %s", verb), c.Text(), ""))
					}
				}
			case "docker", "podman":
				if containsArg(c, "system") && containsArg(c, "prune") {
					out = append(out, finding(self,
						"prunes container state that other work may depend on", c.Text(), ""))
				}
			case "mkfs", "fdisk", "parted":
				out = append(out, finding(self,
					"modifies partition or filesystem structure", c.Text(), ""))
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG013

var ruleFetchThenExecute = Rule{
	ID:       "TG013",
	Title:    "Download followed by execution",
	Severity: SevHigh,
	Rationale: "Splitting a fetch and a run across two commands defeats a rule that " +
		"only looks for pipes, so the two halves are correlated here.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var downloaded []string
		var out []Finding
		for _, c := range s.Commands {
			if fetchers[c.Base()] {
				for i, a := range c.Args() {
					if a == "-o" || a == "-O" || a == "--output" {
						if i+1 < len(c.Args()) {
							downloaded = append(downloaded, path.Base(c.Args()[i+1]))
						}
					}
				}
				for _, r := range c.Redirects {
					if r.Writes() {
						downloaded = append(downloaded, path.Base(r.Target.Literal))
					}
				}
				continue
			}
			for _, name := range downloaded {
				if name == "" {
					continue
				}
				if c.Base() == "chmod" && containsArgSuffix(c, name) {
					out = append(out, finding(self,
						fmt.Sprintf("makes the downloaded file %q executable", name), c.Text(), ""))
				}
				if path.Base(c.Base()) == name || containsArgSuffix(c, name) &&
					(interpreters[c.Base()] || strings.HasPrefix(c.Name(), "./")) {
					out = append(out, finding(self,
						fmt.Sprintf("runs the downloaded file %q", name), c.Text(),
						"read the file before executing it"))
				}
			}
		}
		return out
	},
}

func containsArgSuffix(c Command, name string) bool {
	for _, a := range c.Args() {
		if path.Base(a) == name {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------------- TG014

var ruleSecurityControlDisable = Rule{
	ID:       "TG014",
	Title:    "Security control disabled",
	Severity: SevCritical,
	Rationale: "Turning off a protection to make a command succeed converts a caught " +
		"failure into an uncaught one.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			joined := strings.ToLower(c.Base() + " " + strings.Join(c.Args(), " "))
			for _, pat := range []string{
				"setenforce 0", "iptables -f", "ufw disable", "spctl --master-disable",
				"set-mppreference -disablerealtimemonitoring", "csrutil disable",
				"gsettings set org.gnome.system", "--no-verify", "--insecure",
				"--disable-gpg-check", "sslverify false", "verify=false",
			} {
				if strings.Contains(joined, pat) {
					out = append(out, finding(self,
						fmt.Sprintf("disables a protection (%s)", pat), c.Text(),
						"fix the underlying failure rather than suppressing the check"))
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG015

var ruleBlockDeviceWrite = Rule{
	ID:       "TG015",
	Title:    "Raw write to a device",
	Severity: SevCritical,
	Rationale: "Writing to a block device bypasses the filesystem and every recovery " +
		"mechanism built on top of it.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		for _, c := range s.Commands {
			if c.Base() != "dd" {
				continue
			}
			for _, a := range c.Args() {
				if strings.HasPrefix(a, "of=/dev/") && !strings.HasPrefix(a, "of=/dev/null") &&
					!strings.HasPrefix(a, "of=/dev/stdout") {
					out = append(out, finding(self,
						fmt.Sprintf("dd writes directly to %s", strings.TrimPrefix(a, "of=")),
						c.Text(), ""))
				}
			}
		}
		return out
	},
}

// --------------------------------------------------------------------- TG016

var ruleForkBomb = Rule{
	ID:       "TG016",
	Title:    "Resource exhaustion",
	Severity: SevHigh,
	Rationale: "A machine that has stopped responding cannot be asked whether it " +
		"approved the command that stopped it.",
	Check: func(self Rule, s *Script, _ *Context) []Finding {
		var out []Finding
		compact := strings.ReplaceAll(s.Source, " ", "")
		if strings.Contains(compact, ":(){:|:&};:") || strings.Contains(compact, ":(){ :|:& };:") {
			out = append(out, finding(self, "fork bomb", s.Source, ""))
		}
		for _, c := range s.Commands {
			if c.Base() == "yes" && c.PipeTo {
				out = append(out, finding(self, "unbounded output generator", c.Text(), ""))
			}
			if c.Base() == "dd" && containsArgPrefix(c, "if=/dev/zero") && containsArgPrefix(c, "of=") &&
				!containsArgPrefix(c, "count=") {
				out = append(out, finding(self, "unbounded dd fills the disk", c.Text(), ""))
			}
		}
		return out
	},
}

func containsArgPrefix(c Command, prefix string) bool {
	for _, a := range c.Args() {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
