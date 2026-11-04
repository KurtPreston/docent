# Best practices

Read this before changing docent.

## Docent is public and generic

Docent is a public project. Many of its features start from one person's
private workflow, but docent itself must stay useful to anyone and reveal
nothing about where a feature came from.

- **Build the generic tool, not the use case.** When a feature is motivated by
  a particular organization, repository, team, or workflow, add the general
  capability it needs (a collector option, a trigger field, a template
  variable) and let configuration select the particular case. No code path
  should exist for one specific repository, team, or path.
- **Keep specifics in configuration.** Names, paths, prompts, and policies for
  a particular use belong in the user's own `~/.config/docent/config.yaml`, or
  in a local script a `shell` action runs, never in this repository.
- **Use placeholders everywhere public.** Docs, recipes, tests, examples,
  commit messages, plans, and pull requests use stand-ins such as
  `my-org/my-repo`, `my-team`, and `services/api/`. Never name a private
  organization, repository, team, host, service, or person. Deleted files stay
  in history, so this applies to temporary files such as plans too.

Why: a generic capability serves every user and keeps docent free of special
cases, and keeping private details out protects the people and organizations
whose workflows inspired a feature.

## CLI and daemon boundaries

- **`docent` is the user interface.** Put maintenance, diagnostics, and local
  lifecycle commands here. Running it without a command should show help.
- **`docentd` is the foreground daemon.** Service managers and the CLI launch
  it; it does not dispatch user commands. Diagnostic flags such as `--help`
  and `--version` still belong on the daemon.
- **During v0.x, prefer clean breaks with explicit migrations.** Docent is
  still in its early phases, so command boundaries can change without
  compatibility aliases when the migration is documented. Revisit this
  policy before v1.0; do not assume the same freedom to break compatibility
  once stable interfaces are published. Reject obsolete commands with an
  actionable error; never interpret them as a request to start a daemon.

These boundaries keep maintenance commands usable without a running daemon
and make it clear which process owns background work.

## Release-based rollout

Roll out changes through a reviewed pull request, merge, and published
release, then run `docent update` on installed machines. Do not replace
working installations with ad hoc branch builds: released artifacts keep
versions consistent and exercise the supported upgrade path.

A merge alone does not publish binaries; the release workflow runs on
version tags. Wait for its artifacts before updating. Check for active jobs
before allowing the updater to restart a daemon; use `--restart never` when
installation must be separated from a later, safe restart.
