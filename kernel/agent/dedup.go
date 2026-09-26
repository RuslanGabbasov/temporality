package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// rawArgumentsHash identifies operations whose arguments never parsed as
// JSON: the canonical form is undefined, so the raw string is hashed instead.
func rawArgumentsHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// dedupSignature builds the key collapsing repeat tool calls within one model
// response. For run_command the key is the effective command: shell wrappers
// and cosmetic prefixes normalize to the final command the same way knowledge
// attribution does (echo-prefixed and shell-wrapped forms are repeats of the
// same underlying command). Other tools key on their exact arguments.
func dedupSignature(name string, args map[string]any) string {
	if name != "run_command" {
		return name + "::" + operationArgumentsHash(args)
	}
	command, err := stringArgs(args["command"])
	if err != nil {
		return name + "::" + operationArgumentsHash(args)
	}
	return name + "::" + fmt.Sprintf("%q", effectiveCommand(command))
}

// effectiveCommand unwraps shell wrappers onto their tail command and
// normalizes shell syntax into argv. Conservative: when the wrapper content
// is not a plain single command, the original argv stands.
func effectiveCommand(command []string) []string {
	if script, ok := shellScript(command); ok {
		if tail, ok := scriptTailCommand(script); ok {
			return tail
		}
		return command
	}
	return command
}
