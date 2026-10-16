# V2 tools, fixtures and MCP

The `internal/tooling.Backend` contract is shared by the local simulator and the
stdio MCP client. Both support `List(ctx)`, `Call(ctx, name, arguments)` and
`Close()`. The Go code uses the standard library; `rg` and `git` must be on PATH.
The synthetic replay also needs Python: set `INCIDENTAI_PYTHON` to an executable,
or use `python` / `python3` on PATH. Windows app-store aliases are ignored. As a
last fallback, the tool discovers an existing Codex bundled runtime at
`<user-profile>/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/python.exe`.
No runtime is downloaded or installed automatically.

Every investigation call takes a `scenario_id` and an `incident_id`, and accepts
an optional `service` for normalized external adapters. The local
backend binds an incident to its first scenario and rejects later rebinding.
Fixture files are immutable. Approved simulated recovery affects only that
incident's in-memory state. Replaying the same verified recovery is idempotent,
so the platform can reconstruct simulator state from its persisted incident
record after a restart.

| Canonical tool | Additional input | Normalized output |
| --- | --- | --- |
| `get_metrics` | none | `service`, `points[{time,error_rate,latency_ms}]`, `signals`, `metric_units` |
| `search_logs` | optional literal `query` | `entries`, total matching count, `truncated` |
| `get_deployments` | none | `current_version`, `previous_version`, `deployed_at`, `recent` |
| `get_topology` | none | `nodes[{id,health,latency_ms}]`, `edges[{from,to}]` |
| `get_health` | none | `signals`, `metric_units` |
| `search_code` | literal `query` | real ripgrep `matches[{path,line,content}]` |
| `read_file` | repository-relative `path` | `path`, `content` |
| `git_diff` | none | `from_version`, `to_version`, `diff`, `source` |
| `run_reproduction` | none; deployment-regression only | fixed synthetic cohort, code provenance, actual exceptions and both retry-budget outcomes |

`signals` uses percentage points, not fractions, for `db_pool_pct`,
`cache_error_rate`, `provider_error_rate`, `memory_pct`, and `cpu_pct`.
`db_latency_ms` is milliseconds. The deployment case additionally exposes
`retry_limit_before` and `retry_limit_after`.
`provider_error_rate` counts terminal HTTP 5xx responses; it does not measure
transient per-attempt timeouts. `metric_units` makes these semantics explicit.

Five fixtures cover deployment regression, database overload, Redis failure,
payment provider outage and resource pressure. Only the deployment fixture has
a recent configuration release. `git_diff` runs real `git diff --no-index`
against complete three-file immutable `v2.8.0` and `v2.8.1` snapshot directories,
including identical service.py and retry.py, so copying the project
without a nested `.git` directory preserves the evidence chain.

`run_reproduction` executes the fixed `simulation/v2/reproduce.py` program against
the actual repository retry.py. A shared deterministic cohort of 1,000 operations
requires 1/2/3/4/5/6 attempts for 600/180/170/30/12/8 operations. With the two
snapshot retry limits, the real function raises PaymentRetryExceeded 8 times
(0.8%) for limit 5 and 220 times (22%) for limit 2. Both snapshots must have
byte-identical retry.py. The result is explicitly labeled synthetic replay:
it establishes this controlled mechanism, not the incidence of actual production
timeouts. Calls for other scenarios return a not-applicable error.

The local-only `apply_recovery` call accepts `action` with one of `rollback`,
`mitigate_pool`, `bypass_cache`, `failover_provider`, or `restart_replica`.
It validates that the action addresses that fixture's actual fault. It assumes
the platform has already approved the action; it is never exported by the MCP
server and cannot be called by the remote MCP client. Its result is explicitly
marked `simulated`. The resulting metrics include 9%, 3%, and 0.8% recovery
samples. A replica restart is a temporary mitigation, not proof that the
underlying memory-pressure cause has been fixed.

## Connect a stdio MCP server

Start with `config/mcp.example.json`. It launches the included read-only server
with `go run ./cmd/mcp-simulator --root .`. The configured working directory
must be the project root. For repeated use, build a binary and point `command`
at its absolute path with `args: ["--root", "<absolute project root>"]`:

```powershell
go build -o mcp-simulator.exe ./cmd/mcp-simulator
```

`tooling.NewMCP(ctx, config)` performs the real `initialize` request,
`notifications/initialized`, `tools/list`, then `tools/call` exchanges over
newline-delimited JSON-RPC on subprocess stdin/stdout. It negotiates the pinned
`2025-03-26` protocol version, supports paginated tool discovery and text JSON
results, and also accepts structured results from compatible servers. The
client bounds message size, request duration and unsolicited notifications.
Closing stdin requests graceful shutdown; an unresponsive process is killed
after a short grace period. HTTP transport, batch requests, sampling,
subscriptions and interactive authentication are outside this adapter.

`tools` maps each known canonical read tool to a remote tool name and requires
an explicit local `risk: "read"`. When omitted, only the known canonical read
names are discovered. Remote tool annotations never grant write authorization.
Configuration is trusted operator input, because `command` starts a process.
It is not accepted from model output. Production credentials and vendor MCP
servers are not supplied by this project.

To integrate an observability vendor, provide an adapter with the normalized
input/output contracts above; changing the name alone cannot convert arbitrary
vendor schemas. MCP mode is for read-only investigation. A production recovery
connector needs a separate, explicitly approved implementation and verification
path; simulator recovery must never be presented as an external production
operation.

## Executable investigation skills

`LoadSkills(projectRoot)` loads each `skills/*/SKILL.md` together with its
`plan.json`. The plan records an ID, title, incident types, and ordered steps
with agent, tool and arguments. The loader rejects mutation tools in plans.
The platform supplies incident identifiers and substitutes the dominant error
when a `search_code` step has no query.

The mandatory triage plan checks metrics, logs, deployments, health and
topology. Specialists are selected after observations are available. The
platform handles `search_knowledge`, `search_memory`, `plan_recovery` and
`write_postmortem` as coordinator operations; these are not remote tool names.
`plan_recovery` proposes an action for policy evaluation, never executes it.

## Boundaries and verification

Code search passes the query after `--` as a literal pattern, never through a
shell, and refuses to follow symlinks. `read_file` rejects traversal, absolute
paths and Windows alternate streams, verifies resolved symlinks and opens
through `os.Root`. It only returns regular UTF-8 files up to 256 KiB. Git and
ripgrep and the fixed Python replay have ten-second deadlines and bounded output.
Replay uses isolated Python without site packages or bytecode writes; caller
arguments cannot select a command, script, path, workload or retry limit.
Log output has at most
200 entries plus the true matched total.

```powershell
go test ./internal/tooling ./cmd/mcp-simulator
```

Tests execute actual `rg` and `git`, verify recovery isolation and wrong-action
rejection, and re-execute the Go test binary as a real MCP subprocess. They
cover discovery/calls, renamed tools, untrusted risk metadata, malformed JSON,
wrong response IDs, unsupported versions, oversized messages, timeouts,
filesystem escapes and output size limits.

Protocol references: [stdio transport](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports),
[lifecycle](https://modelcontextprotocol.io/specification/2025-03-26/basic/lifecycle),
and [tools](https://modelcontextprotocol.io/specification/2025-03-26/server/tools).
