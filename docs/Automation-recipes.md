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

## Get notified when a collector needs attention

Credentials expire: an Azure CLI login, a PAT, an OAuth token. When that
happens the collector stops returning data, and rules that depend on it go
quiet without saying why. This rule tells you when any directive starts
failing, with the error, which usually says how to fix it (e.g. "run
`az login`").

```yaml
automations:
  - id: notify-collector-errors
    name: Tell me when a collector starts failing
    enabled: true
    trigger:
      type: signal
      kind: collector_error
      # match.text: "login|token|authoriz"  # only auth failures
    actions:
      - type: shell
        command: ~/.config/docent/notify.sh
        args: ["docent collector failing"]
```

The script gets the directive's name in `DOCENT_TITLE` and the error in
`DOCENT_SUMMARY`. It fires once per outage; see
[Triggers](Automations.md#triggers). Make sure the notification path doesn't
depend on the credential that failed.

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
    source: github          # the collector name, not the directive id
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

## Fix flaky E2E tests from Azure Pipelines

CI retries hide flaky Playwright specs: the run passes, so nobody looks. This
rule reacts to every spec that failed or flaked in an Azure Pipelines run, using
the [`azure-pipelines` collector](Reporting.md#azure-pipelines) with
`log_parser: playwright` (see [Log parsers](Reporting.md#log-parsers)). A
write-mode agent decides between two responses:

- **A PR broke the spec.** It comments on that PR with the evidence.
- **The spec is flaky, or fails on the main branch.** It fixes the spec on a
  fresh branch and opens a ready PR.

Each signal covers one spec file in one run, so a single rule run fixes a single
spec. `base_ref` starts the fix branch from the run's branch, or from `main`
for PR runs. Agents for the same fix branch take turns, so a spec that flakes
twice in a day lands on one branch. The prompt tells the agent to check for an
existing PR first.

```yaml
- id: fix-e2e-flakes
  name: Fix failed and flaky E2E specs
  enabled: true
  trigger:
    type: signal
    source: azure-pipelines  # the collector, not the directive id
    kind: test_failure
  actions:
    - type: agent-inline
      provider: copilot
      workdir: worktree
      base_ref: '{{index .Fields "base_branch"}}'
      prompt: |-
        You are an autonomous agent keeping a Playwright E2E suite stable.
        You are in a fresh git worktree of {{.Repo}} on branch {{.Branch}},
        created from {{index .Fields "base_branch"}}.

        Run:      {{.URL}} ({{index .Fields "result"}}, {{index .Fields "reason"}})
        Spec:     {{index .Fields "test_file"}} ({{index .Fields "outcome"}})
        Failed:   {{index .Fields "failed_tests"}}
        Flaky:    {{index .Fields "flaky_tests"}}
        PR:       {{index .Fields "pr_url"}} (head {{index .Fields "pr_head_branch"}}, commit {{index .Fields "commit"}})
        Errors:
        {{index .Fields "errors"}}

        Full logs: {{index .Fields "test_log_urls"}} (fetch with
        `curl -H "Authorization: Bearer $(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv)" <url>`).

        1. Decide which case this is:
           - REGRESSION: the run is for a PR, the spec FAILED (not flaky),
             and the PR's diff plausibly causes it (check with
             `gh pr diff <pr_url>`).
           - FLAKE: everything else, including any flaky outcome and any
             failure on the main branch.

        2. REGRESSION: change nothing and push nothing. Comment once on the
           PR with the failing tests, the error, and the PR change that
           likely causes it. Skip the comment if an equivalent one exists.

        3. FLAKE:
           a. If an open PR already fixes this spec (search with
              `gh pr list --search "<spec file>"`), stop and report it.
           b. Find the root cause (racy waits, animation timing, shared
              state, order dependence, unscoped locators) and fix it in the
              test or the app. Never just add retries, raise timeouts, or
              skip the test.
           c. Run the project's lint and type checks. Commit, then
              `git push origin HEAD:refs/heads/{{.Branch}}`.
           d. Open a ready (non-draft) PR against
              {{index .Fields "base_branch"}} that explains the flake, with
              a link to the run.

        Begin every PR description and comment with "🤖". Finish with a
        two-line summary: what you did, and the PR or comment URL.
    - type: shell
      command: ~/.config/docent/notify.sh
      args: [e2e]
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

## Auto-reply to review comments on your PRs

When a reviewer leaves a comment on one of your PRs that is waiting on you,
an agent reads it, decides whether to accept or reject it, and replies in the
thread. Clear-cut threads are resolved (accepted changes are pushed first);
anything ambiguous gets a reply but stays open for you. Its summary is then
posted to Slack.

`unresolved_mine_latest` is the Unix time of the newest comment among open
threads whose latest comment isn't yours (`0` when there are none), so
`change: increase` fires on every new comment or reply awaiting you. It still
rises when another thread is resolved in the same collect, where the count in
`unresolved_mine` would hold steady. The agent's own replies post as you, so
they don't raise it and don't re-trigger the rule. The thread list in
`unresolved_threads` is a snapshot. The prompt tells the agent to re-read the
live threads, so a second run that queued behind the first won't answer
anything twice.

The Slack step relays the agent's final message through `.AgentOutput`. Set
`channel` to a channel ID, or your member ID for a DM.

```yaml
- id: autoreply-pr-comments
  name: Auto-reply to review comments on my PRs
  enabled: true
  trigger:
    type: transition
    source: github
    kind: pr_review_status
    match:
      fields:
        mine: "true"
    when:
      field: unresolved_mine_latest
      change: increase
  conditions:
    self: true
  actions:
    - type: agent-inline
      provider: copilot
      workdir: worktree
      prompt: |-
        You are an autonomous review-response agent in a git worktree checked
        out on the head branch of a pull request authored by the user you act
        for. The working directory is the repository root.

        Pull request: {{.URL}}
        Repository:   {{.Repo}}
        Branch:       {{.Branch}}
        Title:        {{.Title}}

        Unresolved review threads when this fired (JSON snapshot, may be stale):
        {{index .Fields "unresolved_threads"}}

        1. Re-read the live threads (owner/name/number come from the URL):
             gh api graphql -F owner=OWNER -F name=NAME -F number=NUMBER -f query='
               query($owner:String!,$name:String!,$number:Int!){
                 repository(owner:$owner,name:$name){ pullRequest(number:$number){
                   reviewThreads(first:100){ nodes{ id isResolved path line
                     comments(first:50){ nodes{ author{login} body url } } } } } } }'
           Handle only threads that are unresolved and whose LAST comment is
           not by `gh api user -q .login` and does not begin with
           "🤖 docent autoreply:". If there are none, do nothing else.

        2. For each, read the comment and the code it points at, then classify:
           - ACCEPT: a clear, low-risk request you agree with that has one
             obvious implementation (bug, typo, naming, docs, small refactor,
             missing test or check).
           - REJECT: clearly incorrect or already handled, and you can show
             why from the code.
           - UNCLEAR: anything else — design or product decisions, broad or
             risky changes, ambiguity, or a question only the author can answer.

        3. Apply every ACCEPT change. Build/test/lint what you touched with the
           project's own tooling; if a change cannot be made to pass simply,
           revert it and treat the thread as UNCLEAR. Then:
             git add -A && git commit -m "Address review feedback"
             git push origin HEAD:{{.Branch}}
           Never force-push, push elsewhere, or open a PR.

        4. Reply in each thread:
             gh api graphql -F thread=THREAD_ID -F body=BODY -f query='
               mutation($thread:ID!,$body:String!){
                 addPullRequestReviewThreadReply(input:{
                   pullRequestReviewThreadId:$thread, body:$body}){ comment{ url } } }'
           - ACCEPT: say what changed and cite the commit SHA, then resolve.
           - REJECT: explain why, then resolve.
           - UNCLEAR: give your analysis and the open question; the author will
             follow up. Do NOT resolve.
           Resolve with:
             gh api graphql -F thread=THREAD_ID -f query='
               mutation($thread:ID!){ resolveReviewThread(input:{threadId:$thread}){
                 thread{ isResolved } } }'
           Begin every reply with "🤖 docent autoreply:".

        5. Your final message is posted to Slack verbatim. Output only a short
           summary, one bullet per thread:
             • ACCEPTED | REJECTED | NEEDS INPUT — `file:line` @reviewer: gist →
               what you did, or the question for the author. <reply url>
           If nothing needed handling, output only: No new review comments to handle.
    - type: slack-post
      channel: U0123456789   # your Slack member ID (DM) or a channel ID
      body: |-
        🤖 *docent autoreply* — <{{.URL}}|{{.Title}}>
        {{.AgentOutput}}{{if .ActionError}}
        :warning: {{.ActionError}}{{end}}
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
