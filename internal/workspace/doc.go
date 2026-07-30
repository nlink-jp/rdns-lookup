// Package workspace writes file-mediated MCP results into an agent-supplied
// directory. A single lookup can return tens of thousands of records, which
// would swamp an agent's context if returned inline, so above a threshold the
// records are written as JSONL and only the path and a count summary come back.
//
// Containment is doubled deliberately: a lexical check rejects absolute paths
// and .. components, and the write itself goes through os.OpenRoot so the
// kernel refuses an escape even if the lexical check were wrong. The directory
// comes from tool arguments, which are attacker-reachable in an agent setting.
package workspace
