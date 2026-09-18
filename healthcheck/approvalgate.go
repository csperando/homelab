package main

import "strings"

// gatedCommandPatterns is the fixed, hardcoded set of Bash command
// substrings that require approval before executing — decided in goal.md:
// git commit, git push, rm -rf at minimum. Not configurable this phase.
var gatedCommandPatterns = []string{
	"git commit",
	"git push",
	"rm -rf",
	"rm -fr",
}

// isGatedCommand reports whether command (a Bash tool call's
// tool_input.command string) matches the fixed gated-action list and must
// be denied pending approval. Substring matching, not anchored to the
// start of the command, so a gated action embedded in a compound command
// (e.g. "cd repo && git push origin main") is still caught. This is a
// practical gate for cooperative agent behavior, not an adversarial-proof
// sandbox — Bash is general-purpose (per Phase 4's tool allowlist), so a
// determined agent could construct ways around simple pattern matching.
func isGatedCommand(command string) bool {
	for _, p := range gatedCommandPatterns {
		if strings.Contains(command, p) {
			return true
		}
	}
	return false
}
