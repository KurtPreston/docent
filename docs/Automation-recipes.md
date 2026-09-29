# Automation recipes

Worked examples of rules under `automations:` in `~/.config/docent/config.yaml`.
Each one is a starting point: copy it, then change the paths, repos, labels, and
prompt details to match your own workflow. See [Automations](Automations.md) for
the full reference on triggers, conditions, and action types.

Paths below use `~/docent/` for report output and `~/.config/docent/notify.sh`
for an optional notification script. Replace both with your own.

## Notifying yourself

Several recipes finish with a `shell` action that runs a small script to tell
you the automation fired, for example by DMing yourself a link to the report or
to the PR comment the agent posted. Docent doesn't ship this script. Write one
that fits your chat tool, taking the report path or PR as arguments. Delete the
step if you don't want notifications.

## Weekly goal-alignment report

Every Friday afternoon, compare the week's activity against `goals.yaml` and
write a markdown report. `deliver: file` has no external side effects.

```yaml
- id: weekly-goal-alignment
  name: Weekly goal alignment report
  enabled: true
  trigger:
    type: schedule
    at: "16:00"
    weekday: friday
  actions:
    - type: report
      mode: goal-alignment
      days: 7
      deliver: file
      out_path: '~/docent/goal-alignment-{{.FiredAt.Format "2006-01-02"}}.md'
      # Optional: layer your team's workflow onto the generic prompt so the
      # model judges progress the way your team does.
      context: |-
        When interpreting tickets, understand this developer's role: their
        job is to get tickets through code review and into "QA". Once a ticket
        reaches "QA" (or any later status such as Done/Closed), the developer's
        work on it is complete. Treat such tickets as finished work. Do NOT
        describe them as unfinished or stalled.
    - type: shell
      command: ~/.config/docent/notify.sh
      args:
        - report
        # Must match out_path above so the link targets the file this run wrote.
        - '~/docent/goal-alignment-{{.FiredAt.Format "2006-01-02"}}.md'
```

The `context` block is where most of the tailoring happens. Use it to tell the
model where your responsibility for a ticket ends, which statuses count as done,
or what kind of work matters most.

## Daily standup plan

On weekday mornings, write a daily-plan report so it's waiting when you sit down.

```yaml
- id: daily-standup
  name: Daily standup plan
  enabled: true
  trigger:
    type: schedule
    cron: "0 5 * * 1-5"
  actions:
    - type: report
      mode: daily-plan
      deliver: file
      out_path: '~/docent/standup-{{.FiredAt.Format "2006-01-02"}}.md'
    - type: shell
      command: ~/.config/docent/notify.sh
      args:
        - report
        - '~/docent/standup-{{.FiredAt.Format "2006-01-02"}}.md'
```

## Scheduled external script

Any command can run on a schedule. This one runs a PR-digest tool on weekday
mornings. If the same job later moves to a shared scheduler, set
`enabled: false` here so it doesn't run twice.

```yaml
- id: pr-review-digest
  name: PR review digest
  enabled: true
  trigger:
    type: schedule
    cron: "0 6 * * 1-5"
  actions:
    - type: shell
      command: ~/go/bin/my-pr-digest
      args: ['--config', '~/.config/my-pr-digest/config.json']
```

## Auto-fix failing CI on your PRs

When CI checks on one of your open PRs turn failing, docent checks out the PR
branch in an isolated worktree and gives it to a write-mode agent. The agent
pushes a fix if the fix is mechanical. Otherwise it only comments.

This needs git push over HTTPS with `gh` as the credential helper
(`gh auth setup-git`). `cooldown` applies per PR and per transition, so repeated
polls stay quiet.

```yaml
- id: autofix-pr
  name: Auto-fix my failing PRs
  enabled: true
  trigger:
    type: transition
    source: github          # the id of your GitHub collector directive
    kind: pr_review_status
    when:
      field: checks
      to: failing
  conditions:
    self: true
    cooldown: 30m
  actions:
    - type: agent-inline
      provider: cursor
      workdir: worktree
      prompt: |-
        You are an autonomous CI-fixing agent running inside a fresh git
        worktree already checked out on the head branch of a pull request
        whose CI is failing. The working directory is the repository root.

        Pull request: {{.URL}}
        Repository:   {{.Repo}}
        Branch:       {{.Branch}}
        Title:        {{.Title}}

        You may edit files and run shell commands (git, gh, and the project's
        build/test tooling). Do not change remotes or credentials.

        1. Diagnose the failure with `gh pr checks {{.URL}}` and
           `gh run view --log-failed`, then reproduce it locally with the
           project's own tooling (package.json scripts, make targets,
           `go test ./...`, the configured Python lint/test command, etc.).

        2. Decide whether the fix is STRAIGHTFORWARD (lint/format errors,
           typos, missing imports, trivial breakage with one clear fix) or
           NOT (ambiguous, needs a design decision, changes behavior in a
           non-obvious way).

        3a. STRAIGHTFORWARD: make the minimal fix, re-run checks locally,
            then commit and push to the SAME branch:
              git add -A && git commit -m "fix CI: <short description>"
              git push origin HEAD:{{.Branch}}
            Never push to main, force-push, or open a new PR. Then comment
            on the PR with what was wrong and what changed.

        3b. NOT STRAIGHTFORWARD: change nothing and push nothing. Comment
            with what is failing, the likely root cause, and the decision a
            human needs to make.

        Never touch unrelated files. Begin every PR comment with
        "🤖 docent autofix:" so it is clearly identified as automated.
    - type: shell
      command: ~/.config/docent/notify.sh
      args: [pr]
```

## Auto-resolve merge conflicts on your PRs

This follows the same pattern as the CI fixer, but the trigger is the PR's
`mergeable` state turning to conflicting. It fires even when the build is green.

```yaml
- id: autofix-pr-conflicts
  name: Auto-resolve merge conflicts on my PRs
  enabled: true
  trigger:
    type: transition
    source: github
    kind: pr_review_status
    when:
      field: mergeable
      to: conflicting
  conditions:
    self: true
    cooldown: 30m
  actions:
    - type: agent-inline
      provider: cursor
      workdir: worktree
      prompt: |-
        You are an autonomous merge-conflict-resolving agent in a fresh git
        worktree checked out on the head branch of a pull request that is
        CONFLICTING with its base. The working directory is the repo root.

        Pull request: {{.URL}}
        Repository:   {{.Repo}}
        Branch:       {{.Branch}}
        Title:        {{.Title}}

        1. Reproduce the conflict:
             base="$(gh pr view {{.URL}} --json baseRefName -q .baseRefName)"
             git fetch origin "$base" && git merge --no-edit FETCH_HEAD
           If it merges cleanly, the status was stale: push nothing, leave a
           brief comment, and stop.

        2. Decide whether EVERY conflict is STRAIGHTFORWARD (independent or
           adjacent edits, import/dependency lists, lockfiles, changelogs,
           generated files, whitespace, one side a superset of the other) or
           whether ANY is NOT (semantically competing changes to the same
           logic, or the right combined behavior is unclear).

        3a. All STRAIGHTFORWARD: resolve each conflict by combining the intent
            of both sides. Never blindly drop one side. Confirm no markers
            remain (`! git grep -nE '^(<<<<<<<|=======|>>>>>>>)'`), build and
            test with the project's tooling, then:
              git add -A && git commit --no-edit
              git push origin HEAD:{{.Branch}}
            Never push to the base branch, force-push, or open a new PR.
            Comment with which files conflicted and how each was resolved.

        3b. Any NOT STRAIGHTFORWARD: run `git merge --abort` and push nothing.
            Comment with the conflicting files, the competing change on each
            side, and the decision a human needs to make.

        Begin every PR comment with "🤖 docent autofix:".
    - type: shell
      command: ~/.config/docent/notify.sh
      args: [pr]
```

## Review and approve clean bot-generated backports

This recipe suits repos where a CI bot cherry-picks merged PRs onto release
branches and assigns the backport PRs to the original requester. The agent
checks each backport and, if it is a clean carry-over, approves it and sets it
to squash-merge. Otherwise it comments to explain why a human still needs to
look at it.

Two pieces work together. First, the GitHub collector needs an extra PR query,
because the bot, not you, is the author:

```yaml
directives:
  - id: github
    collector: github-enterprise
    config:
      pr_queries:
        - relation: backport
          qualifiers: author:app/my-backport-bot assignee:@me
```

Second, the rule matches on that `relation` label:

```yaml
- id: backport-review
  name: Review and approve clean backport PRs
  enabled: true
  trigger:
    type: transition
    source: github
    kind: pr_review_status
    match:
      fields:
        relation: backport
    # No `from:` on purpose. A PR that is new to the snapshot reads as an
    # empty old value, so this fires both when a ready backport first appears
    # ("" -> false) and when a conflicted draft is later marked ready
    # (true -> false).
    when:
      field: is_draft
      to: "false"
  conditions:
    # This rule approves and merges, so pin it to the repos you expect.
    repos:
      - my-org/my-repo
    cooldown: 1h
  actions:
    - type: agent-inline
      provider: cursor
      workdir: worktree
      prompt: |-
        You are an autonomous reviewer for bot-generated backport PRs, running
        in a fresh worktree on the backport's head branch.

        Pull request: {{.URL}}
        Repository:   {{.Repo}}
        Branch:       {{.Branch}}
        Title:        {{.Title}}

        This rule is review-only: do NOT edit, commit, or push. Sibling rules
        handle failing CI and merge conflicts.

        STEP 1: Confirm it is a clean backport:
          gh pr view {{.URL}} --json number,title,body,isDraft,labels,mergeable,author,headRefName,baseRefName,statusCheckRollup,reviews,autoMergeRequest
        DECLINE (step 4) if any of these hold:
          - The author isn't the backport bot, or the head branch doesn't
            follow the bot's naming (e.g. `backport/`).
          - The PR is a draft, or its title marks unresolved conflicts.
          - The label your bot adds only for zero-conflict cherry-picks is
            missing.
          - The body says conflicts were resolved automatically.
          - `mergeable` is CONFLICTING, or `git grep` finds conflict markers.
        If `mergeable` is UNKNOWN, wait about 15s and re-read, up to 3 times.
        If you already approved it or auto-merge is already set, stop without
        commenting.

        STEP 2: Compare the change with the original PR, which the bot links
        in the body:
          gh pr diff {{.URL}}
          gh pr diff <original>
        Raise a concern for anything the original doesn't explain: extra
        files, newer APIs or refactors leaking onto the older branch,
        dependency bumps, migrations, flags, or config defaults that mean
        something different on this release. Confirm every symbol and import
        the change uses exists on this branch.

        STEP 3: If it is a faithful carry-over, comment with your review,
        approve with `gh pr review --approve`, then:
          - checks pending or failing -> `gh pr merge {{.URL}} --squash --auto`
          - all checks passed         -> `gh pr merge {{.URL}} --squash`
        If merging is rejected, leave the approval in place and explain why
        in a comment.

        STEP 4: DECLINE. Don't approve, merge, or change code. Post one
        comment naming the check that stopped you and what a human should do.

        Begin every comment with "🤖 docent backport-review:" and keep it short.
    - type: shell
      command: ~/.config/docent/notify.sh
      args: [pr]
```
