# Model Context Protocol (MCP) evaluation

## Role in slakkr-ai

Slakkr-ai is intentionally **local-first** with deterministic collectors (CLI, HTTP, `gh`, git) and file-backed userdata. **MCP is not required** for the core attention loop: directives, status cache, daybook, and planning providers.

## When MCP might help later

- **Cursor integration**: Expose read-only tools (today’s plan, open delegations, last status snapshot) to an agent without duplicating HTTP in the IDE.
- **Thin adapters**: If your employer standardizes on MCP servers for Jira or Slack, slakkr-ai could call those servers instead of maintaining parallel REST collectors—but only if it reduces auth and operational burden.
- **Agent-to-agent**: If you run multiple agents and want a stable RPC boundary, MCP can sit *beside* userdata, not replace it.

## What to avoid

- Do not make MCP the **system of record** for schedules, plans, or delegation state; keep that in `userdata/`.
- Do not introduce MCP until the **CLI daybook loop** is stable, or you risk debugging two moving parts at once.

## Recommendation

Defer an MCP server until after personal dogfooding. If you add it, scope it to **read-only context export** and **bounded actions** (for example, “return planning JSON for date X”) backed by the same `internal/workflow` code paths as the CLI and `serve`.
