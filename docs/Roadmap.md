# Slakkr-AI Attention Governor Roadmap

## Product direction

Slakkr-ai is a **local-first attention governor**: it helps choose what deserves attention next and makes AI delegation intentional. It does not replace Jira, GitHub, Outlook, a task manager, or Cursor.

## First milestone: personal dogfood

The first useful milestone is daily personal use. The initial project is **slakkr-ai** itself, with tasks to continue building the tool. For that loop, **local git**, **Gitea**, and **Ollama** matter most. **Google Calendar** is secondary. After a few days, the **professional** workflow adds **GitHub Enterprise** and **JIRA** as primary sources, with **Slack** secondary.

## Technical anchors

- `README.md` — userdata layout, recipes/directives, daybook, AI boundary
- `internal/userdata/models.go` — projects, tasks, directives, config
- `internal/collectors/` — normalized `StatusItem` and collectors
- `internal/ai/` — `PlanningInput` / `PlanningOutput`, providers
- `internal/cli/app.go` — `start_day`, `update_status`, `end_day`

## Phases

0. **Direction** — This document and repo alignment.
1. **Personal loop** — Seed userdata; status-cache; attention classification; local git; Gitea; Ollama; then Google Calendar.
2. **Structured daily plan** — Primary/secondary focus, follow-ups, deferrals, non-goals; render to daybook.
3. **Planning + providers** — Richer contracts; configurable `rule-based`, `cursor`, `ollama`.
4. **Delegation ledger** — States: `candidate`, `ready`, `active`, `needs_review`, `accepted`, `rejected`, `superseded`.
5. **Web UI** — Extract workflow services from CLI; local server after CLI proves useful.
6. **MCP (optional)** — Adapter/exposure only when core is stable.
7. **Professional sources** — GitHub Enterprise, JIRA, then Slack.

## Success criteria

- **Focus**: Does it help confidently pick the next thing and ignore the rest?
- **Delegation**: Can you delegate without losing track of multiple agents?
