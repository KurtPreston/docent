# Plan: releases

## Goal

Publish compiled docent releases on GitHub and add `docentd update`, so docent
runs and stays current on machines without a Go or Node toolchain. Follows the
precedent of grove's GoReleaser pipeline and self-update command.

## Scope

In:

- Version stamping for every docent binary, and a `docentd version` command.
- A GoReleaser pipeline triggered by `v*` tags. It builds `docentd` with the
  dashboard embedded, plus `docent-tunnel`, `docent-reporter`, and
  `docent-setup`, for linux and darwin on amd64 and arm64. Each platform gets
  one archive, and the release includes a `checksums.txt`.
- A CI job that does a snapshot release build, so the pipeline can't silently
  break before a tag.
- `docentd update`. It downloads the latest release (or a pinned one), verifies
  its checksum, and replaces every docent binary in docentd's directory. It
  then restarts docentd the way it's running now: through systemd, launchd, or
  the `nohup` fallback. If docentd wasn't running, it asks before starting it,
  and only prints how to start it when there's no terminal to ask on.
- Linux and macOS installers download a release by default. `--from-source`
  keeps today's build path.
- Docs for releasing, installing, and updating.

Out:

- Windows release builds and updating on Windows. The Windows installer keeps
  building from source.
- A separate `docent` user CLI. `docentd update` is the stopgap, tracked as a
  follow-up.
- Relaunching docentd automatically after a container restart (follow-up).
- A curl-pipe installer that needs no checkout. The installers still read
  example configs, the Hammerspoon script, and the IDE extension from a clone.
- Signing or notarizing binaries.

## Execution rules

- All work happens on the `releases` branch. One plan, one branch, one commit per phase.
- Prefix every commit message with `releases: `.
- For each phase: implement only its scope, run validation, fix failures, commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- After the last phase: update docs and commit, delete this file and commit,
  open a pull request (draft where supported; ask first only if the
  repository is public), fix each small unrelated bug on its own
  branch from the base branch with its own pull request, then report results
  with every obstacle and workaround, every bug found and how it was handled,
  and how the user can try the changes.
- This repository is public. Keep private organization, repository, host, and
  team names out of code, docs, tests, commits, and the pull request.

## Validation

```sh
go build ./apps/... && go vet ./... && go test ./...
```

Also run `npm run typecheck` in `apps/docentd/web` if the dashboard changes, and
`bash -n` on any changed shell script. Once the config exists, run
`goreleaser check`, then a local `goreleaser release --snapshot --clean`. Use
`go run github.com/goreleaser/goreleaser/v2@latest` if GoReleaser isn't
installed.

Six tests already fail on main on this Mac (`TestLocalGitCollect*` and two in
`libs/worktree`) because of local git config. Treat those as the baseline;
don't count them as regressions.

## User steps

- **After the PR merges:** tag and push the first release from an up-to-date
  `main`:

  ```sh
  git tag v0.1.0 && git push origin v0.1.0
  ```

  Then check that the Release workflow published four archives and
  `checksums.txt` at https://github.com/KurtPreston/docent/releases.
- **To move the always-on workspace onto releases:** it has no Go toolchain
  now, so run the Linux installer from its checkout after pulling. That
  installs the release binaries, and later runs of `docentd update` keep them
  current.

## Phases

### Phase 1: Version stamping

- Add `libs/buildinfo` with `Version`, `Commit`, and `Date` variables that
  ldflags can set. A `String()` function falls back to the module and VCS
  info from `runtime/debug.ReadBuildInfo` for source builds, such as
  `dev (1314b11)`.
- Add a `version` subcommand to `docentd` and a `-version` flag to
  `docent-tunnel`, `docent-reporter`, and `docent-setup`.
- docentd logs its version at startup, and `docentd doctor` reports it.
- Tests for the fallback formatting.

Done when `go run ./apps/docentd version` prints a dev version with the commit.

### Phase 2: Release pipeline

- Add `.goreleaser.yaml`:
  - A `before` hook runs `npm ci` and `npm run build` in `apps/docentd/web`.
  - Four builds, with `CGO_ENABLED=0` and ldflags setting the
    `libs/buildinfo` variables. docentd also builds with `-tags embed`.
  - One archive per platform, `docent_<os>_<arch>.tar.gz`. Each holds all
    four binaries and `README.md`. (The repository has no LICENSE file.)
  - The checksums go in `checksums.txt`.
- Add `.github/workflows/release.yml`, mirroring grove's: it reruns build,
  vet, and test on a `v*` tag, then runs GoReleaser with Node set up.
- Add a `release-snapshot` job to `ci.yml`. It runs
  `goreleaser release --snapshot --clean`, so a broken config fails CI before
  anyone tags.
- Ignore `dist/` at the repository root in `.gitignore`. Leave the
  dashboard's own `dist/` alone.

Done when `goreleaser check` passes and a local snapshot produces four archives
whose `docentd version` prints the snapshot version and serves the embedded
dashboard.

### Phase 3: `docentd update`

- Port grove's `internal/selfupdate` to `libs/selfupdate`, adapted for
  several binaries:
  - Resolve the latest tag from the `/releases/latest` redirect, not the
    rate-limited API.
  - Download `docent_<os>_<arch>.tar.gz` and verify it against
    `checksums.txt`.
  - Replace `docentd` and any of the other three binaries that already sit
    beside it. Each replacement writes a temp file and renames it over the
    old one.
  - Report what changed.
  - `DOCENT_REPO` and `DOCENT_VERSION` override the source repository and
    target version, as `GROVE_REPO` and `GROVE_VERSION` do for grove.
- Add `libs/selfupdate/restart.go`, which detects how docentd is running and
  restarts it the same way:
  - **systemd:** `docentd.service` is active →
    `systemctl --user restart docentd.service`.
  - **launchd:** `com.docent.docentd` is loaded → `launchctl kickstart -k`.
    It does the same for `com.docent.docent-tunnel` when that binary was
    updated.
  - **nohup fallback:** a running process whose executable is the replaced
    binary. On Linux, it reads that process's arguments and stdout target
    from `/proc`, stops it, and relaunches it detached with the same
    arguments and log file. This matches the installer's `--start-only`.
  - **Not running:** if stdin is a terminal, it asks "Start docentd now?".
    Otherwise it prints the start command. `--restart=never` skips this
    step entirely.
- Add a `docentd update` subcommand with `--version TAG`, `--force`, and
  `--check`. `--check` reports whether an update exists without installing.
  If the running build isn't a release and `--force` isn't set, it refuses.
  A source build has no version to compare.
- Tests use an httptest release server, as grove's tests do. They cover tag
  resolution, checksum mismatch, the up-to-date check, multi-binary
  replacement, and the restart detection decisions, with the system calls
  stubbed.

Done when updating a snapshot install from a local test server replaces the
binaries and restarts a `nohup`-launched docentd on the same port.

### Phase 4: Installers download releases

- `install-docent-linux.sh` and `install-docent-macos.sh` gain
  `--version TAG` and `--from-source`.
  - By default they download the release archive for this platform, verify
    its checksum, and install the binaries into the bin directory. This
    replaces the Go and Node builds.
  - With `--from-source`, they keep the current build path.
- Share the download logic. Put a `scripts/lib/fetch-release.sh` helper, if
  it stays simple, in both installers. That way neither installer duplicates
  grove's curl/wget, tag resolution, and checksum code.
- Building the IDE extension still needs Node. If Node is missing, the
  installers skip it with a note.
- macOS host discovery uses the downloaded `docent-tunnel` instead of
  building one.
- The Windows installer is unchanged.

Done when both scripts pass `bash -n`, and the Linux installer installs a
working daemon from a release with no Go on PATH. Test this in a Linux
container against a snapshot or test release.

## Obstacles

## Found bugs
