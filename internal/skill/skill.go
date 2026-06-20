// Package skill embeds the axprobe-author skill — the codified rubric for
// authoring and reviewing AX fixtures — so it ships with the binary and can be
// printed or installed as a Claude Code skill.
package skill

import _ "embed"

//go:embed SKILL.md
var Body string

// Name is the skill's directory name when installed under .claude/skills/.
const Name = "axprobe-author"
