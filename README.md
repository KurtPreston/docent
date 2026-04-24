# slakkr-ai

`slakkr-ai` is a local-first project operations tool for keeping track of projects, tasks, daily plans, status updates, and work that might be delegated to an AI agent.

The current implementation is a Go CLI with reusable packages behind the command layer, so a future local UI can call the same workflows.

## Quick Start

```sh
go test ./...
go run ./cmd/slakkr list-recipes
go run ./cmd/slakkr setup --yes
go run ./cmd/slakkr start_day
go run ./cmd/slakkr update_status
go run ./cmd/slakkr end_day
```

The tool writes user-owned data under `userdata/`. That directory is gitignored by this repo and initialized as its own local git repository. Setup can optionally add a remote for `userdata`, but local-only is the default.

## Data Layout

- `recipes/` contains reusable directive recipes that can be shared by the public project or an organization-specific fork.
- `userdata/projects.yaml` stores durable project records.
- `userdata/tasks.yaml` stores active work, priority, status, links, next actions, and delegation suitability.
- `userdata/directives.yaml` stores local status-gathering directives instantiated from recipes.
- `userdata/daybook/YYYY-MM-DD.md` stores daily goals, status snapshots, plans, and reflections.
- `userdata/status-cache/` is reserved for normalized or raw status snapshots.

## Commands

- `setup` initializes `userdata`, discovers recipes, and can interactively instantiate directives.
- `start_day` gathers status, creates today's daybook entry, and proposes focus blocks and delegation candidates.
- `update_status` gathers status and appends a status snapshot to today's daybook.
- `end_day` gathers final status, appends reflection prompts, and attempts to commit `userdata`.
- `validate` checks user data schemas and references.
- `list-recipes` shows reusable recipes available in this checkout or fork.

## Scripts

The `scripts/` wrappers match the initial product sketch:

```sh
scripts/setup
scripts/start_day
scripts/update_status
scripts/end_day
```

## AI Boundary

Deterministic code loads YAML, validates schemas, discovers recipes, gathers status, writes daybook files, and commits user data. AI providers synthesize that bounded context into plans, reflection questions, and delegation suggestions. The default provider is rule-based and deterministic for tests; `internal/ai` also defines a Cursor CLI provider boundary for later live-agent integration.
