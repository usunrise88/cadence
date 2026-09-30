package policy

import (
	"path"
	"regexp"
	"strings"
)

// PermissionRequest is what an agent asked to do in its ACP permission request, as the agent host reads it: the
// tool class (mcp, edit, shell, read, search, fetch, think, other), the Cadence operation of an MCP call and its
// verb class, the shell command, and the paths relative to the worktree.
type PermissionRequest struct {
	Class     string
	Operation string
	VerbClass string
	Command   string
	Paths     []string
}

// PermissionAnswer is the preset's answer: allow and deny are decided here, ask goes to a person.
type PermissionAnswer struct {
	Access Access
	Rule   string // what decided: tools.<rule id>, files.edit, files.deny, shell.allow, shell.default, web, …
	Reason string
}

// AnswerPermission answers an agent's own permission request from the preset (R7: "a policy engine answers
// requests the table already decides, so the user sees only real decisions"). It never widens what the preset
// allows: Cadence tools follow the tool rules the server enforces anyway, files the file rules, the shell its
// patterns (deny beats ask beats allow), fetches the web rule; anything else asks a person.
func (p *Preset) AnswerPermission(r PermissionRequest) PermissionAnswer {
	for _, pth := range r.Paths {
		if pat, ok := p.Files.denied(pth); ok {
			return PermissionAnswer{Access: AccessDeny, Rule: "files.deny", Reason: "the preset never lets agents touch " + pat}
		}
	}
	switch r.Class {
	case "mcp":
		verbClass := r.VerbClass
		if verbClass == "" {
			verbClass = "mutate"
		}
		c, rule := p.ClassOf(r.Operation, verbClass)
		if agentSide(c) == AccessAllow {
			return PermissionAnswer{Access: AccessAllow, Rule: "tools." + rule,
				Reason: "Cadence decides this command itself (" + string(c) + "); gated ones answer with an approval"}
		}
		return PermissionAnswer{Access: AccessDeny, Rule: "tools." + rule, Reason: "the preset does not allow " + r.Operation}
	case "edit":
		return fileAnswer(p.Files.Edit, AccessAsk, "files.edit", "editing files in the worktree")
	case "read", "search":
		return fileAnswer(p.Files.Read, AccessAllow, "files.read", "reading files in the worktree")
	case "shell":
		return p.Shell.answer(r.Command)
	case "fetch":
		return fileAnswer(p.Web, AccessAsk, "web", "web access")
	case "think":
		return PermissionAnswer{Access: AccessAllow, Rule: "think", Reason: "planning needs no permission"}
	}
	return PermissionAnswer{Access: AccessAsk, Rule: "other", Reason: "the preset has no rule for this kind of tool"}
}

func fileAnswer(a, def Access, rule, what string) PermissionAnswer {
	if a == "" {
		a = def
	}
	return PermissionAnswer{Access: a, Rule: rule, Reason: "the preset says " + string(a) + " for " + what}
}

// denied reports the deny pattern a worktree-relative path matches (gitignore-like: a pattern without a slash
// matches the base name at any depth; **/ and /** match any number of directories).
func (f FileRules) denied(p string) (string, bool) {
	p = strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/")
	if p == "" || p == "." {
		return "", false
	}
	base := path.Base(p)
	for _, pat := range f.Deny {
		switch {
		case strings.HasSuffix(pat, "/**"):
			dir := strings.TrimSuffix(pat, "/**")
			if p == dir || strings.HasPrefix(p, dir+"/") {
				return pat, true
			}
		case strings.HasPrefix(pat, "**/"):
			if ok, _ := path.Match(strings.TrimPrefix(pat, "**/"), base); ok {
				return pat, true
			}
		case !strings.Contains(pat, "/"):
			if ok, _ := path.Match(pat, base); ok {
				return pat, true
			}
		default:
			if ok, _ := path.Match(pat, p); ok {
				return pat, true
			}
		}
	}
	return "", false
}

// answer applies the shell rules to one command line: deny beats ask beats allow; anything else is the default.
func (s ShellRules) answer(command string) PermissionAnswer {
	cmd := strings.TrimSpace(command)
	for _, set := range []struct {
		access Access
		pats   []string
	}{{AccessDeny, s.Deny}, {AccessAsk, s.Ask}, {AccessAllow, s.Allow}} {
		for _, pat := range set.pats {
			if shellMatch(pat, cmd) {
				return PermissionAnswer{Access: set.access, Rule: "shell." + string(set.access),
					Reason: "the preset's shell rule " + pat + " says " + string(set.access)}
			}
		}
	}
	def := s.Default
	if def == "" {
		def = AccessAsk
	}
	return PermissionAnswer{Access: def, Rule: "shell.default", Reason: "no shell rule matches; the preset's default is " + string(def)}
}

// shellMatch matches a command against a pattern whose * stands for any text. A command with shell control
// characters (;, &&, |, $(…), backquotes, redirections) matches no pattern, so it falls to the default: `ls *` must
// not allow `ls; curl …`.
func shellMatch(pat, cmd string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pat), `\*`, ".*") + "$"
	ok, _ := regexp.MatchString(re, cmd)
	if !ok {
		return false
	}
	return !strings.ContainsAny(cmd, ";|&`$()<>\n")
}
