# ADR-0001: The MCP protocol layer becomes nlink-jp/mcp — both eras

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-28) |
| Date | 2026-11-28 |
| Binds | rdns-lookup |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 (every MCP implementation speaks both eras and takes its protocol layer only from nlink-jp/mcp); issue nlink-jp/rdns-lookup#1 |

## Context

`internal/mcp/server.go` was a hand-written stdio MCP server speaking the legacy era only (the `initialize`
handshake), so a modern-only client of the 2026-07-28 revision could not reach it. rdns-lookup is a member of family A
(lookup, first line; nlink-jp/mcp's server-migration §1), whose reference implementation is asn-lookup (its
ADR-0001, which also records the re-measured peers of 2026-11-27: Claude Code 2.1.311, gem-agent and lagent are all
dual-era).

## Decision

Follow the common policy (nlink-jp/mcp's `docs/en/server-migration.md`, v0.3.0) as asn-lookup does:

1. **The shape seen from outside stays (policy §2).** `mcp.Serve` keeps its arguments and gains a leading `ctx`, which the policy's signal rule needs (§2). Inside it is a
   `server.ToolServer`: the tools are registered in `toolsList()` order and each handler calls
   `toolsCall(ctx, name, args)`. The hand-written read/write loop, handshake and `rpcError` go.
2. **Options (policy §2)**: `Workers` 1, `QueueDepth` 256, a `slog` to stderr (Warn and above). Name, version and
   instructions as today.
3. **Tool failures (policy §3)**: strict decoding through `decodeArgs` and the isError texts stay; argument names are
   now matched exactly.
4. **Behaviour that changes (policy §4)**: as listed in the CHANGELOG — `tools/*` and unknown methods before initialize
   are -32602; `arguments` that is not an object is -32602; `"id":null` and a request without `"jsonrpc"` are -32600;
   framing changes from `json.Decoder` to one message per line (16 MiB); parse errors and batches no longer end the
   server; a panic is answered -32603; up to 256 calls wait, more get -32603 "server busy".
5. **Cancellation (policy §5)**: `notifications/cancelled` now cancels the call's ctx. A cancelled lookup still runs to its end — the engine takes no context — and its answer is dropped; the cache is written whole to a temporary file and renamed, so nothing partial is left.
6. **Signals (policy §2)**: the `mcp` subcommand passes `Serve` a ctx cancelled by SIGINT and SIGTERM.
7. **Dependency and version**: `github.com/nlink-jp/mcp` is added; minor (v0.4.0).
8. **Peer stand-ins (ADR-030 §6)**: `scripts/e2e.sh` keeps its `printf` streams as a legacy-era peer, and each now opens with `initialize`, since `tools/*` before it is refused.

## Defects found (each fixed in its own fix commit)

- An argument spelt in another case (`{"IP_Address": ...}`, or `Limit` of the shared arguments) was accepted as the declared one; it is now refused by name.
- SIGINT or SIGTERM ended the MCP server by killing the process; a signal now ends it through its context, so its cleanup runs.
- `scripts/e2e.sh` still checked the file spill retired in v0.2 (`workspace_root`, `records_file`), so it could not pass; it now checks that `workspace_root` is refused by name and that records come back inline with no file written.
- `scripts/e2e.sh`'s "nothing indexed" check used 203.0.113.9, which the index now holds a record for; it uses 233.252.0.77 (MCAST-TEST-NET).

## Consequences

- Modern-only clients reach it too; legacy clients are answered as before.
- Protocol-layer updates arrive through the module.
- Tests replay their requests through the module's `client`; tests of protocol details went (the module tests them);
  a test against the three probe clients was added.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Add `server/discover` to the hand-written layer | Leaves the copy of the handshake that ADR-030 §6 forbids |
| Keep `QueueDepth` at the default 16 | The 17th call sent side by side would be refused as "server busy"; before the move it waited |

## References

- Organization ADR-030, ADR-025, ADR-021 §4
- nlink-jp/mcp v0.3.0: `docs/en/server-migration.md`
- asn-lookup ADR-0001 (family A's reference)
- Issue nlink-jp/rdns-lookup#1
