# Plan: cli-daemon-split

## Goal

Separate the user-invoked `docent` CLI from the foreground `docentd` daemon.
Add local lifecycle commands and make releases, updates, and installers
consistently deliver the new interface.

## Scope

- `docent version`, `doctor`, `install-hooks`, and `update` replace the
  corresponding `docentd` subcommands.
- Add `docent start`, `stop`, `restart`, and `status` for the local daemon on
  Linux and macOS. No command remotely controls another machine.
- `docent` with no arguments prints help. Unknown commands fail with usage.
  `docentd` runs in the foreground with its existing daemon flags; retain
  diagnostic version/help flags, but remove user subcommands without aliases.
- Preserve configuration formats and paths, service names, ports, daemon
  behavior, and the dashboard. No new daemon HTTP control API is needed.
- Keep setup, reporting, and tunnel binaries separate. Do not consolidate
  `docent-setup` or `docent-reporter` in this effort.
- Windows release/update/lifecycle support remains deferred. Keep source
  builds working and wire the new CLI into the Windows source installer so
  moved maintenance commands remain available.
- A documented one-time installer migration is acceptable. The v0.1.0 updater
  does not know the new CLI binary; it is not the supported migration path.

## Execution rules

- All work happens on the `cli-daemon-split` branch in its own worktree.
  One plan, one branch, one commit per phase.
- Prefix every commit message with `cli-daemon-split: `.
- For each phase: implement only its scope, run validation, fix failures,
  commit.
- Read this plan and `docs/best-practices.md` before implementing each phase.
- Phase 1 is a behavior-preserving extraction. Do not combine the extraction
  of oversized files with subsequent behavior changes.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs under Found bugs. Fold in fixes belonging to this effort,
  save small unrelated fixes for separate branches, and leave nuanced ones
  unfixed for discussion.
- After the last phase: update docs and commit, delete this file and commit,
  ask before pushing and opening a public pull request, fix each small
  unrelated bug independently, and report results, obstacles, and user steps.
- Do not deploy, change installed configuration, or restart an installed
  daemon during implementation. Use test doubles and isolated containers.

## Command behavior

- Locate installed binaries relative to the resolved CLI executable, not an
  arbitrary `docentd` found on PATH. Resolve CLI and daemon paths separately.
- Lifecycle operations act only on the current user's matching installation.
  An unrelated service with the standard label must never be controlled.
  Ambiguous instances produce a diagnostic rather than a guessed target.
- `start` is idempotent if already running. Otherwise start through an
  installed systemd user unit or launchd agent; use the existing detached
  Linux fallback when there is no manager. Explicit `start` is consent to
  start the daemon.
- `stop` is idempotent if stopped. Stop the manager, not just its child:
  unload a launchd agent so KeepAlive does not immediately revive it, while
  retaining its plist for the next start. Do not disable services permanently.
- `restart` restarts a running daemon and starts a stopped one. Preserve the
  launch arguments, working directory, environment, and logging of a supported
  unmanaged Linux process. Refuse unsafe hand-started macOS relaunches.
- `status` never changes state. Report running/stopped/not-installed and the
  manager/PID when known. Exit 0 for running, 3 for stopped/not-installed,
  and 1 for an inspection error. Missing relaunch metadata alone must not
  prevent reporting an otherwise identifiable process.
- Use installed service definitions rather than silently overriding them.
  A config-directory option can select a new unmanaged launch; conflicting
  overrides for managed or already-running instances must fail explicitly.
- Verify start/restart completion with bounded readiness checks, using the
  effective local endpoint when resolvable and confirmed process identity.
  A successful manager command alone is not proof of a healthy daemon.
- Prevent concurrent mutating commands from starting duplicates or racing an
  update/restart. Reuse an existing locking mechanism if suitable.
- Local lifecycle commands do not start a daemon on a remote-only client:
  report the absence of the local daemon and how to install it.

## Validation

After each implementation phase:

```sh
go build ./apps/... && go vet ./... && go test ./...
```

Run targeted tests first. The known unrelated worktree-fixture failures are
tracked separately: when required on macOS, rerun with a canonical TMPDIR and
command-local `safe.bareRepository=all`, recording both results. Never change
global Git settings to make tests pass.

Also run, where relevant:

- CLI argument/exit-code tests and shared config/diagnostic tests.
- Fake systemd/launchd tests covering stopped services, wrong installations,
  KeepAlive-safe stop, repeat starts/stops, and command failures.
- Linux integration tests as an unprivileged user, with explicit child
  readiness handshakes, verifying start/status/stop/restart and actual health.
- Updater tests covering paired replacement, rollback, CLI-only installs,
  installed sibling selection, checksums, and stopped-daemon confirmation.
- Bash syntax checks, ShellCheck for changed shell helpers, and installer
  dry runs using temporary homes and mocked service commands.
- Windows cross-builds and PowerShell syntax validation when available;
  explicitly report unavailable native checks.
- A GoReleaser snapshot with archive-content and embedded-dashboard checks.
- `npm run typecheck` in `apps/docentd/web` only if that code changes.

Stop all test processes and remove generated temporary artifacts before
reporting or handing off.

## User steps

- **Plan approval:** approve this plan before implementation starts.
- **After implementation:** approve the exact public branch push and PR
  description. No public write is authorized by approving this plan alone.
- **After merge:** publish the next release, proposed as `v0.2.0` for the
  command-interface break. From a clean checkout, after confirming the merged
  commit is on the default branch:

  ```sh
  git fetch origin
  git tag v0.2.0 origin/main
  git push origin refs/tags/v0.2.0:refs/tags/v0.2.0
  ```

- **After that release succeeds:** from an updated main checkout, rerun the
  applicable installer once, preserving any existing installation options:

  ```sh
  bash scripts/install-docent-linux.sh --version v0.2.0
  # or, on macOS:
  bash scripts/install-docent-macos.sh --version v0.2.0
  ```

  These commands can restart installed services; only the user runs them
  against real deployments. Then check `docent version` and `docent status`.
  Future updates use `docent update`, not `docentd update`.

## Phases

### Phase 1: Extract shared configuration and daemon-control code

- Move daemon config loading into `libs/config/daemonconfig`, including its
  tests. Preserve the existing internal package's exported names with thin
  re-exports where needed to keep this a mechanical move.
- Extract directive defaults from the oversized engine file into the shared
  config area. Keep `engine.EnsureDirectives` importable without duplicating
  the implementation. The CLI must not import or instantiate the engine.
- Move process inspection and service lifecycle primitives out of
  `libs/selfupdate` into `libs/daemoncontrol`, including their test seams.
  Keep the updater's behavior unchanged and update imports/re-exports.
- Extract binary-build/install portions of oversized platform installers
  into focused adjacent helpers before changing them in Phase 4. Move only
  the sections this effort needs; do not broadly rewrite the installers.

Validation: required Go checks, config/default regression tests, daemon
restart tests, installer syntax checks, and behavior-preserving dry runs.
Commit the extraction separately from all behavior changes.

### Phase 2: Introduce the CLI and move maintenance commands

- Add `apps/docent` with a testable command runner and explicit exit codes.
  Use the standard flag package and existing conventions; no new CLI framework.
- Move doctor, hooks, version, and update entry points to the CLI.
  Retain offline diagnostics: a running daemon is not required.
- Remove user-command dispatch from `docentd`. Reject positional arguments,
  including old subcommands, with migration guidance before loading config
  or starting background work.
- Make the updater explicitly target the CLI and any installed local daemon.
  Require the CLI payload; when a daemon is installed, require and stage both
  binaries before replacing either. Preserve checksum and rollback guarantees.
- Preserve installed sibling selection for the separate tools. A client-only
  installation updates its CLI/tools without installing or starting a daemon.
  Reject old archives lacking `docent` before changing anything.
- Reuse shared daemon control for update restarts. Restart only a previously
  running daemon; retain confirmation before starting a stopped local daemon.
  Do not offer daemon startup on client-only installations.
- Update runtime remediation messages and directly related command comments.

Validation: CLI help/invalid-argument/exit-code tests; doctor and hooks parity;
old-command rejection without daemon startup; updater path-resolution,
CLI-only, paired replacement, failure/rollback, and restart tests; required
Go checks and Windows cross-build.

### Phase 3: Add local lifecycle commands

- Implement `docent start`, `stop`, `restart`, and `status` with the behavior
  defined above, using `libs/daemoncontrol` rather than shelling out to another
  CLI or duplicating the updater's process handling.
- Separate read-only inspection from the ability to reconstruct a launch.
  Do not make status fail merely because restart would require a manual step.
- Handle loaded-but-stopped launchd agents, unloaded agents with retained
  plists, installed but stopped systemd units, and unmanaged Linux processes.
- Make safety checks and readiness/error handling consistent between explicit
  lifecycle operations and `docent update`.
- Report unsupported platforms explicitly while keeping all applications
  buildable. Do not add Windows service-control support in this phase.

Validation: manager-state matrix and failure-path tests, bounded startup
failure tests, idempotence/concurrency tests, and unprivileged Linux
end-to-end start/status/restart/stop checks, followed by required Go checks.

### Phase 4: Ship and install the new interface

- Add `docent` to GoReleaser's stamped platform builds and release archives.
  Keep dashboard embedding restricted to `docentd`.
- Update release archive validation to require the CLI. Keep installer
  downloads and verification ahead of service shutdown.
- Install/build CLI plus daemon for local Linux/macOS deployments. Install
  CLI plus the selected tools for remote-only macOS clients, without adding
  a local daemon.
- Build/install `docent.exe` in the Windows source path where applicable;
  keep its existing daemon/service behavior and document unsupported commands.
- Wire hook installation and maintenance guidance to `docent`, including
  source-build paths. Extend macOS uninstall cleanup to the new binary.
- Keep existing service definitions launching `docentd`, not `docent`.
- Update archive/installer tests to catch missing CLI payloads and mismatched
  CLI/daemon installations. Verify migration from an old installation using
  the new installer, without relying on the old updater.

Validation: required Go checks, cross-builds, installer syntax/lint and dry
runs, snapshot archive inspection, and Go-less Linux installer/update tests
against a local release fixture. Do not touch real installed services.

## Documentation and completion

After Phase 4, update README, dashboard/automation guides, installer usage,
and any remaining command references. Include lifecycle semantics, exit
codes, local-only behavior, and the one-time migration. Preserve historical
old commands only where explicitly describing migration.

Commit documentation, remove this plan in its own commit, and request approval
for the public push and PR. The implementation and every phase remain on
`cli-daemon-split`.

## Obstacles

- Existing Go `internal` boundaries prevent a separate CLI from importing
  daemon config directly; Phase 1 creates shared packages instead of coupling
  the CLI to the daemon application.
- The old updater's fixed binary list cannot bootstrap `docent`; use the
  approved clean installer migration, not a compatibility shim.
- Lifecycle detection currently also checks relaunchability. Phase 3 separates
  those concerns so inspection and stop can work without reconstructing the
  entire process environment.

## Found bugs

- **Needs discussion; tracked separately:** existing worktree fixtures depend
  on host Git settings and canonical temporary paths. Do not fix them on this
  branch or weaken production Git safeguards.
