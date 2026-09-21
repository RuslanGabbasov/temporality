package coding

import (
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

// Parser builds the coding-world call parser: it flattens the four coding
// tools into the layer's ParsedCall shape, inferring the subsystem scope from
// the command/path itself and falling back to the task's scope.
func Parser(lexicon []string) memory.CallParser {
	return func(call llm.ToolCall, taskService string) memory.ParsedCall {
		parsed := memory.ParsedCall{Name: call.Name, Params: map[string]any{}}
		switch call.Name {
		case "shell":
			command, _ := call.Args["command"].(string)
			parsed.Params["command"] = command
			parsed.Service = InferScope(command, lexicon)
		case "read_file":
			path, _ := call.Args["path"].(string)
			parsed.Params["path"] = path
			parsed.Service = InferScope(path, lexicon)
		case "grep":
			pattern, _ := call.Args["pattern"].(string)
			path, _ := call.Args["path"].(string)
			parsed.Params["pattern"] = pattern
			if path != "" {
				parsed.Params["path"] = path
			}
			parsed.Service = InferScope(pattern+" "+path, lexicon)
		case "list_files":
			path, _ := call.Args["path"].(string)
			if path != "" {
				parsed.Params["path"] = path
			}
			parsed.Service = InferScope(path, lexicon)
		}
		if parsed.Service == "" {
			parsed.Service = taskService
		}
		return parsed
	}
}
