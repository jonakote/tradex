# Hermes MCP connection

`tradex-mcp` exposes the safe Tradex research surface over MCP stdio. It is
designed for Hermes or another MCP client to inspect the strategy, validate and
freeze experiment artifacts, run deterministic paper replays, and build
self-verifying reports.

It deliberately does not expose live order placement, account credentials, API
key values, or broker mutation tools. Live execution remains behind the
existing broker safety and readiness gates.

Build it from the repository root:

```sh
go build -o ./bin/tradex-mcp ./cmd/tradex-mcp
```

Configure Hermes with a stdio server entry similar to:

```json
{
  "mcpServers": {
    "tradex": {
      "command": "/absolute/path/to/tradex/bin/tradex-mcp",
      "env": {
        "TRADEX_WORKSPACE": "/absolute/path/to/tradex",
        "HERMES_SPEC_ROOT": "/absolute/path/to/hermes_specs"
      }
    }
  }
}
```

The server exposes these tools:

- `tradex_status`
- `tradex_validate_artifact`
- `tradex_freeze_manifest`
- `tradex_run_replay`
- `tradex_run_shared_replay`
- `tradex_build_report`

It also exposes the README, trading-bot plan, implementation-status, and
triangle-pattern specification files, plus every Markdown document under
`HERMES_SPEC_ROOT`, as read-only MCP resources.
Output artifacts are created with exclusive file creation; existing manifests,
audits, results, and reports are never overwritten.
