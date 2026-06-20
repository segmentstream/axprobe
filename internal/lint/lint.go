// Package lint flags tool-interface leakage in a fixture goal. A good fixture
// goal is user-level intent: naming the tool's own commands, flags, internal
// response states, or transport mechanics turns the AX test into a spoon-fed
// script and stops measuring whether the tool guides the agent itself. These are
// heuristic warnings, not hard errors.
package lint

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	flagRe  = regexp.MustCompile(`(?:^|\s)(--[a-zA-Z][\w-]+)`)
	stateRe = regexp.MustCompile(`(?i)\b([\w.]+\s*[:=]\s*(?:true|false))\b`)
	exitRe  = regexp.MustCompile(`(?i)\b(exit(?:\s+code)?\s+\d+)\b`)
	wordRe  = regexp.MustCompile(`[a-zA-Z][\w-]*`)
	// jargon: transport/mechanism words describing HOW a tool works. Whole-word
	// matched so a dataset name like "axprobe_bigquery_oauth" is not flagged
	// (underscore is a word char, so there is no boundary before "oauth"). --json
	// and other flags are handled by flagRe.
	jargonRe = regexp.MustCompile(`(?i)\b(oauth|loopback|callback|stdout|stderr|localhost|127\.0\.0\.1|device[- ]?code|access token)\b`)
)

// stopwords are command-ish tokens too common in plain English to flag as a
// leaked tool command from the run vocabulary.
var stopwords = map[string]bool{
	"run": true, "test": true, "get": true, "list": true, "show": true,
	"use": true, "add": true, "set": true, "update": true, "help": true,
	"make": true, "start": true, "create": true, "build": true, "the": true,
	"check": true, "open": true, "read": true, "write": true, "data": true,
}

// Goal returns warnings about interface leakage in goal. toolVocab is the set of
// the tool's own command tokens (e.g. derived from the commands a run executed);
// it may be empty, in which case only the generic checks run.
func Goal(goal string, toolVocab []string) []string {
	var warns []string
	seen := map[string]bool{}
	add := func(kind, tok string) {
		tok = strings.TrimSpace(tok)
		key := kind + "|" + strings.ToLower(tok)
		if tok == "" || seen[key] {
			return
		}
		seen[key] = true
		warns = append(warns, fmt.Sprintf("%s %q — describe the user outcome, not the tool's interface", kind, tok))
	}

	for _, m := range flagRe.FindAllStringSubmatch(goal, -1) {
		add("names a flag", m[1])
	}
	for _, m := range stateRe.FindAllStringSubmatch(goal, -1) {
		add("names an internal state", m[1])
	}
	for _, m := range exitRe.FindAllStringSubmatch(goal, -1) {
		add("names an internal state", m[1])
	}

	for _, m := range jargonRe.FindAllString(goal, -1) {
		add("uses transport jargon", strings.ToLower(m))
	}

	low := strings.ToLower(goal)
	vocab := vocabSet(toolVocab)
	for _, word := range wordRe.FindAllString(low, -1) {
		if vocab[word] {
			add("names a tool command", word)
		}
	}
	return warns
}

// vocabSet reduces raw command strings to the distinctive SUBcommand tokens worth
// flagging. For each command it skips the binary (first field — often a path like
// /root/.../segmentstream) and stops at the first flag, so flag VALUES (the user's
// own project/dataset names) and the binary/product name are never treated as tool
// commands. Only the words between binary and the first flag — the subcommands —
// count (non-stopword, length>=3).
func vocabSet(toolVocab []string) map[string]bool {
	set := map[string]bool{}
	for _, raw := range toolVocab {
		for i, f := range strings.Fields(raw) {
			if i == 0 {
				continue // binary / path
			}
			if strings.HasPrefix(f, "-") {
				break // first flag → flags and their values are not commands
			}
			f = strings.ToLower(f)
			if len(f) >= 3 && !stopwords[f] {
				set[f] = true
			}
		}
	}
	return set
}
