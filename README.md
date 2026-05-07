# slakkr-ai

Local-first CLI: collect **time-bounded activity** from configured sources, send it to an AI provider, and get a **Markdown** document (printed to stdout and saved under `userdata/output/`).

## Quick start

```sh
go test ./...
go run ./cmd/slakkr --help
# First run creates userdata/config.yaml if missing
go run ./cmd/slakkr --mode recent-activity --days 3
```

Or use [`scripts/slakkr`](scripts/slakkr) from the repo root.

## Configuration (`userdata/config.yaml`)

Single file: `ai` and `directives`.

- **`directives`**: Collector, target, config, `credential_refs` for secrets in `userdata/.env`.
- **`local-git`**: Use **`paths`** for explicit repo roots, or **`code_home`** to scan that directory’s immediate children that contain `.git`.
- **Legacy**: If `directives` is missing but `userdata/directives.yaml` exists, directives are loaded from there once.

Example:

```yaml
ai:
  provider: ollama   # or cursor, rule-based
  ollama:
    base_url: http://127.0.0.1:11434
    model: llama3

directives:
  - id: local-git
    name: Local repos
    collector: local-git
    enabled: true
    code_home: /Users/me/Code
  - id: github-me
    name: GitHub
    collector: github-activity
    enabled: true
    target:
      username: MyGitHubUser
    config:
      base_url: https://github.com
    credential_refs:
      token: SLAKKR_GITHUB_TOKEN
```

## Modes

| Mode | Lookback | Behavior |
|------|----------|----------|
| `daily-plan` | Previous weekday 00:00 → now (Mon/weekends → last Fri) | Optional “priorities today” prompt; AI output should use `## Yesterday` and `## Today`. |
| `recent-activity` | `--days N` (default 7, or prompt) | Summarize activity; grouped markdown. |
| `custom-prompt` | `--days N` | `--prompt` / `--prompt-file` / interactive prompt; model follows your instructions. |

Run without `--mode` on a TTY to pick interactively.

### Common flags

- `--userdata DIR` (default `userdata`)
- `--out PATH` — default `userdata/output/<date>-<mode>.md`
- `--no-save` — stdout only
- `--date YYYY-MM-DD` — label for default output filename only

## AI providers

- **`rule-based`**: Deterministic markdown (no network); good for tests.
- **`ollama`**: HTTP chat to Ollama; streams to stderr when connected to a TTY.
- **`cursor`**: Shells out to `cursor-agent -p <payload>` (override with `ai.cursor.command` / `args`).

## Collectors

All collectors run in **date range** mode (`since` → `until`). Implemented: `local-git` (commits + reflog), `github` / `github-enterprise`, `github-activity`, `gitea`, `jira`, `google-calendar`. Manual / slack collectors were removed.

## Layout

- `userdata/config.yaml` — only required config file.
- `userdata/output/` — saved markdown.
- `userdata/.cache/ai-debug/` — optional Ollama request/response logs.

The `userdata/` directory is gitignored; initialize by running the CLI once or copy a config by hand.
