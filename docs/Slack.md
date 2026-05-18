# Slack collector

The `slack` collector pulls Slack messages relevant to the configured user
(DMs, mentions, your own posts, and — at higher scopes — surrounding
threads/channels). It authenticates with a **Slack User OAuth token**
(`xoxp-...`); a Bot token (`xoxb-...`) will not work and `slakkr --check`
rejects it explicitly because bot tokens cannot read your DMs or run
`from:@me` searches.

## What gets collected

The signal set is driven by the run's [scope](../README.md#scope-semantics):

| Scope | Signals |
|-------|---------|
| `self` | DMs you received, `@`-mentions of you, messages you sent. |
| `involved` (default) | Everything from `self`, plus the full content of any thread you posted in, plus the 3 messages immediately before/after each of your messages in its channel (context window). |
| `all` | Everything from `involved`, plus every message in channels listed in `config.followed_channels`. |

`is_self` is `true` for your own messages and for items addressed to you
(DM, mention). Thread replies authored by other people and context-window
messages get `is_self=false`, so `recent-activity` only shows your own
posts while `daily-plan` / `involved` runs see the surrounding context.

Each emitted `StatusItem` uses one of these `kind` values:
`slack_dm`, `slack_mention`, `slack_sent`, `slack_thread_reply`,
`slack_context`, `slack_channel_message`. Items are grouped by channel in
the formatter (`#team-foo`, `dm:alice`, `mpim:alice,bob`).

## Generating a Slack token

### 1. Create a Slack app

1. Go to <https://api.slack.com/apps> and click **Create New App** → **From scratch**.
2. Name it something like `slakkr-personal` and pick the workspace you want activity from. (You need one app per workspace; add a second `slack` directive if you collect from multiple workspaces.)

### 2. Add user-token scopes

In the app's left sidebar, open **OAuth & Permissions** and scroll to
**Scopes**. Under **User Token Scopes** (the *bottom* table — not "Bot
Token Scopes"), add all of these:

- `search:read`
- `users:read`
- `channels:history`, `channels:read`
- `groups:history`, `groups:read`
- `im:history`, `im:read`
- `mpim:history`, `mpim:read`

The `slackTokenScopesRemediation` constant in
[`internal/collectors/slack.go`](../internal/collectors/slack.go) is the
source of truth for this list — `slakkr --check` will print it back at
you if a scope is missing.

### 3. Install the app to the workspace

Scroll back up on **OAuth & Permissions** and click **Install to Workspace**.
Approve the OAuth consent screen. After install, the page shows two tokens;
copy the one labeled **User OAuth Token** (it starts with `xoxp-...`). The
`xoxb-...` Bot User token will *not* work.

### 4. Wire it into slakkr

Drop the token into `userdata/.env` (the file `userdata.ResolveEnv` reads from):

```sh
SLAKKR_SLACK_TOKEN=xoxp-XXXXXXXXXXXX-XXXXXXXXXXXX-...
```

The default directive in `userdata/config.yaml` already references this env
var via `credential_refs.token: SLAKKR_SLACK_TOKEN`. Flip its
`enabled: false` to `true`, then verify with:

```sh
slakkr --check
```

A clean run means `auth.test` succeeded with a user token and all your
scopes resolved.

## Directive reference

```yaml
- id: slack
  name: Slack
  collector: slack
  enabled: true
  config:
    # Optional. Comma/space/newline-separated channel names (#team) or IDs
    # (Cxxxx). Only consulted when scope=all. Names are resolved via
    # conversations.list once per run; IDs are passed through.
    followed_channels: "#team-foo, #team-bar"
    # Optional. Override the user_id resolved from auth.test (rarely needed).
    user_id: ""
    # Optional. How many DM/MPIM conversations.history requests to issue
    # in parallel (default 4, capped at 16). Bump to ~8-10 if your daily
    # plan still feels slow despite skipping inactive DMs; the collector
    # transparently waits on Retry-After if Slack starts rate-limiting.
    history_concurrency: "4"
  credential_refs:
    token: SLAKKR_SLACK_TOKEN
```

## Request shape & throttling

`scope=involved` (the default) issues:

1. One `auth.test` call to resolve the workspace + user identity.
2. One `conversations.list?types=im,mpim` cursor walk to enumerate DMs
   and group DMs you can see.
3. One `conversations.history` per remaining DM/MPIM channel, fanned out
   across `history_concurrency` workers. DMs whose peer user has been
   deactivated (`is_user_deleted: true`) and any archived channels are
   skipped before this fan-out, which on long-lived accounts usually
   eliminates the majority of dead channels.
4. Two `search.messages` calls — one for `@me` mentions and one for your
   sent messages.
5. One `conversations.replies` per unique thread you posted in (involved
   tier), plus two non-inclusive `conversations.history` calls per sent
   message for the 3-before / 3-after context window.
6. One `users.info` per distinct unknown author, best-effort (skipped on
   failure so a rate-limited lookup doesn't drop the message).

If Slack responds with `429 Too Many Requests`, every Slack call now
retries up to 4 times, honoring the `Retry-After` header (capped at 30s
per sleep) and falling back to exponential backoff when the header is
absent. The wait is logged to `<run>/slack.log` as a `note` line so you
can see when throttling kicked in.

`scope: all` only collects extra messages when `config.followed_channels`
is non-empty. Without it, `all` behaves like `involved` for this
collector — same as the other forge/ticket collectors documented in
[README › Following repos / projects in scope: all](../README.md#following-repos--projects-in-scope-all).

## Common gotchas

- **Workspace admin approval.** Many workspaces require an admin to
  approve any new app install. If the **Install to Workspace** button
  shows "Request to Install" instead, you'll need an admin to click
  through. The app itself is private to your workspace and you (the
  installer) get the token — admins don't see your messages just because
  the app exists.
- **`search:read` and free workspaces.** Slack's `search.messages` API
  has historically been gated behind paid plans. If your workspace is on
  the free tier and `search.messages` returns `not_allowed_token_type`
  or similar, the mention/sent paths will fail. The DM and (for
  `scope=all`) followed-channel paths still work because they use
  `conversations.history` instead.
- **Re-installing after scope changes.** If you add a scope later, you
  must click **Reinstall to Workspace** — the existing token won't gain
  the new scope automatically. Copy the freshly minted token back into
  `userdata/.env`.
- **Token rotation.** Revoke from <https://api.slack.com/apps> → your
  app → **OAuth & Permissions** → **Revoke** (or rotate from the same
  page), then reinstall and replace the value in `userdata/.env`.
- **Multiple workspaces.** One Slack token = one workspace. Add a
  second directive with a different `id` (e.g. `slack-other`) and a
  different env var (e.g. `SLAKKR_SLACK_OTHER_TOKEN`) to collect from
  more than one workspace in the same run.
