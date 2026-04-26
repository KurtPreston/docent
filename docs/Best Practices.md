# CLI and architecture best practices

This document captures conventions for `slakkr` so the tool stays predictable, safe, and pleasant in the terminal.

## Stream AI output

Any command that invokes a **local LLM** (today: **Ollama**) should **stream** the model’s response to the user instead of blocking with no output until the full reply is ready. Generation can take tens of seconds or longer; streaming gives immediate feedback and matches a good REPL-style experience.

**Concrete pattern in this repo:**

- After **directive / collector** output is finished (e.g. close the directive progress table on stdout), print a short **stderr** banner and set the Ollama client’s stream writer to **`stderr`** (`a.Err`), not stdout. Many UIs line-buffer stdout differently; keeping model text on stderr avoids mixing with structured stdout and matches `start_day` and `update_tasks`.
- When the model run completes, print a trailing newline on stderr so the next log line is separated cleanly.

Commands that use other providers (rule-based, Cursor CLI) follow the same **“no surprise silent wait”** idea: either return quickly or document that a subprocess may run for a while.

## Proposed task review

After **`update_tasks`** has written `proposed-tasks.yaml`, the CLI (when **stdin is a TTY** and you did not pass `--no-resolve`) **prompts for each still-pending** proposal: create a `tasks.yaml` row, link the linked signals to an existing task, or dismiss. This should stay **interactive and explicit**; real tasks are only created or linked when the user chooses that path.

In CI or when stdin is not a terminal, the review is skipped; use **`--no-resolve`** to avoid the “skipped” message on stderr.

## Directives and collectors are read-only

**Recipes and directives must not mutate** the systems they observe. Collectors are **read** tools: they inventory state and return `StatusItem`s. They must not change:

- Remote issue trackers, mail, calendars, or chat.
- **Git**: do not run commands that alter repository state (e.g. `checkout`, `switch`, `pull`, `push`, `fetch`, `add`, `commit`, `merge`, `rebase`, `stash`, etc.). Use inspection-only commands (`status`, `log`, `rev-parse`, `for-each-ref`, `merge-base`, `rev-list`, …).

Business logic that **writes** userdata (`tasks.yaml`, `signals.yaml`, daybook, etc.) lives in **explicit commands** (e.g. `add_task`, `update_tasks`, `start_day`), not inside collectors.

## Where to look in code

| Concern | Location |
|--------|----------|
| CLI entry, progress table, streaming hooks | `internal/cli/app.go` |
| Ollama streaming (day plan, task signals) | `internal/ai/ollama.go` |
| Task update orchestration | `internal/taskupdate/run.go` |
| Collectors registry | `internal/collectors/` |

For general layout and the **AI boundary**, see [README.md](../README.md) and [docs/Roadmap.md](Roadmap.md).
