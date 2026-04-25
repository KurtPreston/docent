# slakkr-ai

`slakkr-ai` is a local-first project operations tool for keeping track of projects, tasks, daily plans, status updates, and work that might be delegated to an AI agent.

The current implementation is a Go CLI with reusable packages behind the command layer, so a future local UI can call the same workflows.

## Quick Start

```sh
go test ./...
go run ./cmd/slakkr list-recipes
go run ./cmd/slakkr setup
go run ./cmd/slakkr configure_host
go run ./cmd/slakkr add_project
go run ./cmd/slakkr add_task
go run ./cmd/slakkr start_day
go run ./cmd/slakkr update_status
go run ./cmd/slakkr end_day
go run ./cmd/slakkr update_tasks
go run ./cmd/slakkr validate
go run ./cmd/slakkr serve --addr 127.0.0.1:8765
go run ./cmd/slakkr plan show
go run ./cmd/slakkr delegation list
```

The tool writes user-owned data under `userdata/`. That directory is gitignored by this repo and initialized as its own local git repository. Setup asks whether to add a remote for `userdata`; local-only remains the default.

To dogfood quickly, copy [examples/dogfood/](examples/dogfood/) into `userdata/` (adjust `YOUR_HOST` and paths) after `setup`.

## Data Layout

- `recipes/` contains reusable directive recipes that can be shared by the public project or an organization-specific fork.
- `userdata/projects.yaml` stores durable project records.
- `userdata/tasks.yaml` stores active work, priority, status, links, next actions, and delegation suitability.
- `userdata/directives.yaml` stores local status-gathering directives instantiated from recipes.
- `userdata/daybook/YYYY-MM-DD.md` stores daily goals, status snapshots, plans, and reflections.
- `userdata/status-cache/` is reserved for normalized or raw status snapshots.
- `userdata/plans/YYYY-MM-DD.yaml` stores the structured daily plan produced by `start_day`.
- `userdata/delegations.yaml` stores agent delegation ledger entries.
- `userdata/signals.yaml` stores resolved work signals (from collectors) mapped to tasks or ignore decisions.
- `userdata/proposed-tasks.yaml` stores task proposals for later confirmation (from `update_tasks`), before they are promoted into `tasks.yaml`.

A recipe is a reusable template shipped with this repo or a fork. A directive is your local configured instance of a recipe. During setup, the directive id is just the stable key written to `userdata/directives.yaml`; accepting the suggested default is usually right for a first setup.

Targets are the specific thing a directive monitors. The local Git recipe takes `project_id` and `repo_id` and resolves working trees from that repo’s `paths_by_host` in `userdata/projects.yaml` (for the current host; see `SLAKKR_HOST`). A Gitea recipe would use server configuration such as a `base_url` plus a target owner or organization.

Recipes and directives are readonly by design. Collectors may inspect local files, repositories, and external services, but they must not mutate the systems they observe. For local Git repositories, collectors must not run mutating commands such as `git checkout`, `git switch`, `git push`, `git pull`, `git fetch`, `git add`, `git commit`, `git merge`, `git rebase`, or `git stash`; they should use inspection commands only.

Secret recipe fields are prompted as secret values during setup. They are written to `userdata/.env` and referenced from directives through `credential_refs`, so tokens are not stored directly in `userdata/directives.yaml`. `userdata/.gitignore` excludes `.env` before setup commits user data.

Setup asks for a human-readable name, then generates the stable directive id. If a recipe asks for a required field you do not want to provide yet, leave it blank and setup will skip that recipe while continuing with the rest.

## Commands

- `setup` initializes `userdata`, asks about remote persistence, discovers recipes, and can interactively instantiate directives.
- `configure_host` configures host-local defaults (such as `code_home`) and can import projects from `CODE_HOME`.
- `add_project` adds or updates one project in `userdata/projects.yaml`.
- `add_task` adds or updates one task in `userdata/tasks.yaml`.
- `start_day` gathers status, creates today's daybook entry, and proposes focus blocks and delegation candidates.
- `update_status` gathers status and appends a status snapshot to today's daybook.
- `end_day` gathers final status, appends reflection prompts, and attempts to commit `userdata`.
- `update_tasks` runs the same read-only status collectors as the daybook flow, classifies new work signals, updates `userdata/signals.yaml`, and can append to `userdata/proposed-tasks.yaml` (it does not modify `tasks.yaml` automatically). Use `--dry-run` to preview.
- `validate` checks user data schemas and references.
- `list-recipes` shows reusable recipes available in this checkout or fork.
- `serve` runs a small local web UI (JSON APIs for planning input, saved plan, delegations).
- `plan show` prints the structured daily plan YAML from `userdata/plans/` (after `start_day`).
- `delegation list` / `delegation add` manage the agent work ledger in `userdata/delegations.yaml`.

## AI providers

Configure `userdata/config.yaml`:

```yaml
ai:
  provider: rule-based   # or ollama, cursor
  ollama:
    base_url: http://127.0.0.1:11434
    model: llama3
```

Ollama must return a single JSON object matching the planning schema (see `internal/ai`). The rule-based provider remains the default for tests and offline use.

## Scripts

The `scripts/` wrappers match the initial product sketch:

```sh
scripts/setup
scripts/configure_host
scripts/add_project
scripts/add_task
scripts/start_day
scripts/update_status
scripts/update_tasks
scripts/end_day
```

## AI Boundary

Deterministic code loads YAML, validates schemas, discovers recipes, gathers status, writes daybook files, and commits user data. AI providers synthesize that bounded context into plans, reflection questions, and delegation suggestions. The default provider is rule-based and deterministic for tests; `internal/ai` also supports **Ollama** (HTTP) and **Cursor** (`cursor-agent` CLI). See [docs/MCP.md](docs/MCP.md) for optional MCP evaluation.
