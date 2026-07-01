package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// SlackCollector pulls Slack messages relevant to the configured user.
//
// Authentication uses a Slack User OAuth token (xoxp-...). The token's owner
// is resolved via auth.test at collection time and treated as the "self"
// identity. BaseURL defaults to https://slack.com/api and is overridable for
// tests via httptest servers.
type SlackCollector struct {
	Clock   func() time.Time
	HTTP    *http.Client
	BaseURL string
}

// slackTokenScopesRemediation lists the Slack User OAuth scopes the
// collector needs. Surfaced verbatim from validator messages so users can
// fix scope misconfiguration without leaving the CLI.
const slackTokenScopesRemediation = "ensure the Slack app's user-token scopes include: " +
	"search:read, users:read, channels:history, channels:read, " +
	"groups:history, groups:read, im:history, im:read, " +
	"mpim:history, mpim:read"

// Tunables that bound how aggressively the collector talks to Slack.
// These intentionally live as package constants so the test suite can
// observe the same numbers the collector uses at runtime.
const (
	// defaultHistoryConcurrency is the worker count used to fan out
	// per-channel conversations.history calls. Overridable per-directive
	// via Config["history_concurrency"]. Tier-3 endpoints document
	// ~50 req/min, so 4 in-flight workers leaves plenty of headroom for
	// the collector to share with mention/sent/replies traffic.
	defaultHistoryConcurrency = 4
	// maxHistoryConcurrency caps user-supplied overrides so a typo
	// can't burst hundreds of requests/sec into Slack and trigger a
	// long workspace-wide cooldown.
	maxHistoryConcurrency = 16
	// slackMaxRetries is how many times callAPI will sleep on a 429
	// before giving up and surfacing the rate-limit error.
	slackMaxRetries = 4
	// slackMaxRetryAfter caps any single Retry-After sleep; Slack can
	// occasionally return very large values, and we'd rather fail fast
	// than make the user think the CLI is hung.
	slackMaxRetryAfter = 30 * time.Second
	// slackDiscoveryMaxPages bounds the to:<@me> discovery search. Each
	// page is 100 matches, so 20 pages covers 2000 inbound DM messages in
	// the window. If a user somehow exceeds that we treat discovery as
	// incomplete and fall back to polling every DM rather than risk
	// pruning a channel whose only inbound message sat beyond the cap.
	slackDiscoveryMaxPages = 20
)

// slackAPIError represents a Slack-level error (HTTP 200 with `ok:false`
// and an `error` code in the body). Carrying the code as a field — rather
// than burying it in a formatted string — lets callers branch on
// well-known recoverable codes (e.g. channel_not_found on a single DM)
// without resorting to substring matching.
type slackAPIError struct {
	Endpoint string
	Code     string
}

func (e *slackAPIError) Error() string {
	return fmt.Sprintf("slack %s: %s", e.Endpoint, e.Code)
}

// tolerablePerChannelHistoryErrors lists Slack `error` codes that mean
// "this single channel is unreachable" rather than "the whole run should
// abort". Each of these can show up legitimately in the IM/MPIM list
// returned by conversations.list (e.g. a DM whose peer left the
// workspace mid-run, a private channel the user was just removed from)
// and the right behavior is to skip that one channel and keep going.
var tolerablePerChannelHistoryErrors = map[string]struct{}{
	"channel_not_found": {},
	"not_in_channel":    {},
	"is_archived":       {},
	"access_denied":     {},
	"missing_scope":     {}, // single-channel scope gap (rare); whole-token gap shows up on auth.test
}

// isTolerablePerChannelError returns true when err is a slackAPIError whose
// code we should skip rather than propagate.
func isTolerablePerChannelError(err error) bool {
	var apiErr *slackAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	_, ok := tolerablePerChannelHistoryErrors[apiErr.Code]
	return ok
}

func (c SlackCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c SlackCollector) baseURL() string {
	if b := strings.TrimSpace(c.BaseURL); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://slack.com/api"
}

// slackResponseMeta is embedded by every Slack API response wrapper.
type slackResponseMeta struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor,omitempty"`
	} `json:"response_metadata,omitempty"`
}

type slackAuthTest struct {
	slackResponseMeta
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id,omitempty"`
}

type slackChannel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	IsIM          bool   `json:"is_im,omitempty"`
	IsMPIM        bool   `json:"is_mpim,omitempty"`
	IsGroup       bool   `json:"is_group,omitempty"`
	IsPrivate     bool   `json:"is_private,omitempty"`
	IsChannel     bool   `json:"is_channel,omitempty"`
	IsArchived    bool   `json:"is_archived,omitempty"`
	IsUserDeleted bool   `json:"is_user_deleted,omitempty"` // peer in is_im has been deactivated
	User          string `json:"user,omitempty"`            // peer for is_im
	Members       int    `json:"num_members,omitempty"`
}

type slackConversationsList struct {
	slackResponseMeta
	Channels []slackChannel `json:"channels"`
}

type slackUser struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	RealName string `json:"real_name,omitempty"`
	Profile  struct {
		DisplayName string `json:"display_name,omitempty"`
		RealName    string `json:"real_name,omitempty"`
	} `json:"profile,omitempty"`
}

type slackUsersInfo struct {
	slackResponseMeta
	User slackUser `json:"user"`
}

type slackMessage struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype,omitempty"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts,omitempty"`
	Team     string `json:"team,omitempty"`
}

type slackHistory struct {
	slackResponseMeta
	Messages []slackMessage `json:"messages"`
	HasMore  bool           `json:"has_more,omitempty"`
}

type slackReplies struct {
	slackResponseMeta
	Messages []slackMessage `json:"messages"`
}

// slackSearchMatch reflects the slimmed-down message rows returned inside
// search.messages results (the channel and team data is nested differently
// from conversations.history).
type slackSearchMatch struct {
	Type    string `json:"type"`
	User    string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	Text    string `json:"text"`
	TS      string `json:"ts"`
	Team    string `json:"team,omitempty"`
	Channel struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	} `json:"channel"`
	Permalink string `json:"permalink,omitempty"`
}

type slackSearchMessages struct {
	slackResponseMeta
	Messages struct {
		Matches []slackSearchMatch `json:"matches"`
		Paging  struct {
			Page  int `json:"page,omitempty"`
			Pages int `json:"pages,omitempty"`
		} `json:"paging,omitempty"`
	} `json:"messages"`
}

// slackProgress tracks the rolling Completed/Total denominator the
// Slack collector pushes through opts.OnDirectiveUpdate. Slack does
// most of its wall-clock work fanning out conversations.history calls
// across hundreds of DMs and (in involved/all scopes) threads, so the
// denominator is the union of every per-channel/per-thread work unit
// the collector knows about at any moment. New phases call AddUnits
// to extend Total; finished units call Done to advance Completed.
//
// The tracker is safe for concurrent use because the history fan-out
// worker pool calls Done from multiple goroutines at once.
type slackProgress struct {
	opts        *CollectOpts
	directiveID string
	description string
	mu          sync.Mutex
	completed   int
	total       int
	stage       string
}

func newSlackProgress(opts *CollectOpts, directive userdata.Directive) *slackProgress {
	return &slackProgress{
		opts:        opts,
		directiveID: directive.ID,
		description: directive.Name,
	}
}

// SetStage replaces the visible Detail label (e.g. "DMs", "threads",
// "followed channels"). Numbers are unchanged so the bar continues
// moving forward.
func (p *slackProgress) SetStage(stage string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stage = stage
	p.emitLocked()
	p.mu.Unlock()
}

// AddUnits grows the denominator. Used at the start of each fan-out
// phase whose size is known.
func (p *slackProgress) AddUnits(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.mu.Lock()
	p.total += n
	p.emitLocked()
	p.mu.Unlock()
}

// Done advances the numerator by one finished work unit. Safe to call
// from any worker goroutine.
func (p *slackProgress) Done() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.completed++
	if p.completed > p.total {
		// Defensive: never let the bar overshoot if a caller forgot
		// to AddUnits up-front. Bumping Total here keeps the bar at
		// or below 100%.
		p.total = p.completed
	}
	p.emitLocked()
	p.mu.Unlock()
}

func (p *slackProgress) emitLocked() {
	detail := p.stage
	if p.total > 0 {
		if detail == "" {
			detail = fmt.Sprintf("%d/%d", p.completed, p.total)
		} else {
			detail = fmt.Sprintf("%s %d/%d", detail, p.completed, p.total)
		}
	}
	reportProgress(p.opts, DirectiveProgress{
		DirectiveID: p.directiveID,
		Description: p.description,
		Status:      "running",
		Detail:      detail,
		Completed:   p.completed,
		Total:       p.total,
	})
}

// Collect orchestrates the Slack Web API calls required by the resolved
// scope and returns a deduplicated slice of StatusItems.
//
// Scope semantics:
//   - ScopeSelf: DMs the user received, @-mentions of the user, messages the
//     user sent.
//   - ScopeInvolved (default): self UNION the full content of every thread
//     the user has posted in, plus the 3 messages immediately before/after
//     each "self" message (context window). Non-self messages added by the
//     thread/context expansion are flagged IsSelf=false so post-filtering
//     scopes (recent-activity) still see only the user's own posts while
//     involved/all modes see surrounding context.
//   - ScopeAll: involved UNION every message in channels listed in
//     config.followed_channels (resolved by name #foo or ID Cxxxx).
func (c SlackCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	if tokenKey == "" {
		return nil, fmt.Errorf("slack credential missing (set credential_refs.token to a DOCENT_SLACK_TOKEN-style env var)")
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	token := userdata.ResolveEnv(userdataDir, tokenKey)
	if token == "" {
		return nil, fmt.Errorf("slack token env %q is empty", tokenKey)
	}
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	scope := opts.EffectiveScope()
	progress := newSlackProgress(opts, directive)
	progress.SetStage("auth")

	auth, err := c.callAuthTest(ctx, token, opts, directive.ID)
	if err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(directive.Config["user_id"])
	if userID == "" {
		userID = auth.UserID
	}
	if userID == "" {
		return nil, fmt.Errorf("slack auth.test did not return a user_id")
	}
	teamURL := strings.TrimRight(strings.TrimSpace(auth.URL), "/")

	teamID := strings.TrimSpace(auth.TeamID)
	if teamID == "" {
		teamID = strings.TrimSpace(auth.Team)
	}
	persistent := loadSlackCache(userdataDir, teamID)

	channelCache := newSlackChannelCache()
	userCache := newSlackUserCache()
	userCache.put(slackUser{ID: userID, Name: auth.User})

	type collected struct {
		message slackMessage
		channel slackChannel
		kind    string // slack_dm | slack_mention | slack_sent | slack_thread_reply | slack_context | slack_channel_message
		isSelf  bool
		fields  map[string]string
	}
	bucket := []collected{}
	dedupe := map[string]int{} // (channel_id|ts) -> index in bucket

	add := func(item collected) {
		if item.message.TS == "" || item.channel.ID == "" {
			return
		}
		key := item.channel.ID + "|" + item.message.TS
		if existing, ok := dedupe[key]; ok {
			// Prefer the more specific kind on dedupe (e.g. slack_mention
			// over slack_channel_message) and keep IsSelf=true if any
			// source claimed it.
			cur := bucket[existing]
			if slackKindRank(item.kind) > slackKindRank(cur.kind) {
				cur.kind = item.kind
			}
			if item.isSelf {
				cur.isSelf = true
			}
			for k, v := range item.fields {
				if cur.fields == nil {
					cur.fields = map[string]string{}
				}
				if _, exists := cur.fields[k]; !exists {
					cur.fields[k] = v
				}
			}
			bucket[existing] = cur
			return
		}
		dedupe[key] = len(bucket)
		bucket = append(bucket, item)
	}

	// finalize resolves authors for whatever is currently in the bucket
	// and turns it into the sorted StatusItem slice the caller expects.
	// It is invoked both on the normal happy path and when collection is
	// aborted mid-flight (ctx cancelled): in the abort case we keep every
	// message gathered so far and skip any further network work.
	finalize := func() []StatusItem {
		// Resolve unique authors so StatusItem.Author renders as a name
		// when possible, falling back to the bare user ID. When the run
		// has been aborted we stop spending users.info calls and just use
		// cached identities (or the bare ID).
		for _, b := range bucket {
			if b.message.User == "" {
				continue
			}
			if _, ok := userCache.get(b.message.User); ok {
				continue
			}
			// Reuse a previously-resolved identity from the persistent
			// cache before spending a users.info call. Display names
			// change rarely, so this eliminates almost all author lookups
			// on repeat runs.
			if u, ok := persistent.cachedUser(b.message.User, now); ok {
				userCache.put(u)
				continue
			}
			if ctx.Err() != nil {
				userCache.put(slackUser{ID: b.message.User})
				continue
			}
			u, err := c.fetchUser(ctx, token, b.message.User, opts, directive.ID)
			if err != nil {
				// Best-effort: skip on lookup failure (rate limit, missing scope).
				userCache.put(slackUser{ID: b.message.User})
				continue
			}
			userCache.put(u)
			persistent.putUser(u, now)
		}

		if err := persistent.save(); err != nil {
			loggerFor(opts, directive.ID).Note("slack: failed to persist cache: %v", err)
		}

		items := make([]StatusItem, 0, len(bucket))
		for _, b := range bucket {
			obs, err := slackTSToTime(b.message.TS)
			if err != nil {
				continue
			}
			channelKey := slackChannelKey(b.channel, userCache, userID)
			fields := map[string]string{
				"channel":    channelKey,
				"channel_id": b.channel.ID,
				"ts":         b.message.TS,
				"kind":       b.kind,
				"team":       auth.Team,
			}
			if b.message.ThreadTS != "" {
				fields["thread_ts"] = b.message.ThreadTS
			}
			for k, v := range b.fields {
				fields[k] = v
			}
			title := slackTruncate(b.message.Text, 200)
			author := slackAuthorDisplay(b.message, userCache)
			items = append(items, StatusItem{
				DirectiveID: directive.ID,
				Repository:  channelKey,
				Source:      "slack",
				Kind:        b.kind,
				Title:       title,
				Summary:     "",
				URL:         slackPermalink(teamURL, b.channel.ID, b.message.TS),
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				IsSelf:      b.isSelf,
				Author:      author,
				Fields:      fields,
			})
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].ObservedAt.Before(items[j].ObservedAt)
		})
		return items
	}

	// --- self-tier signals (always run) ---

	progress.SetStage("listing DMs")
	dmChannels, err := c.fetchAllConversations(ctx, token, "im,mpim", channelCache, opts, directive.ID)
	if err != nil {
		if ctx.Err() != nil {
			return finalize(), nil
		}
		return nil, fmt.Errorf("slack conversations.list (dm): %w", err)
	}
	// Skip IMs whose peer user has been deactivated and archived MPIMs:
	// they cannot have new messages in the window, so calling
	// conversations.history on them is pure overhead (and contributes
	// to the per-workspace rate-limit budget). For a typical user with
	// hundreds of historical DMs this is often the single biggest cut.
	activeDMs := dmChannels[:0:0]
	skipped := 0
	for _, ch := range dmChannels {
		if ch.IsArchived {
			skipped++
			continue
		}
		if ch.IsIM && ch.IsUserDeleted {
			skipped++
			continue
		}
		activeDMs = append(activeDMs, ch)
	}
	if skipped > 0 {
		loggerFor(opts, directive.ID).Note(
			"slack: skipping %d DM/MPIM channel(s) with deactivated peer or archived state", skipped,
		)
	}

	// Decide which DMs actually need a conversations.history call. By
	// default a single `to:<@me>` search discovers the (usually tiny) set
	// of 1:1 DMs with inbound messages in the window, so we poll only
	// those instead of fanning out across every DM the user can see.
	// MPIMs are always polled (they aren't returned by `to:` searches),
	// and any search failure falls back to polling every DM.
	pollDMs := c.selectDMsToPoll(ctx, token, userID, since, activeDMs, opts, directive, progress)

	progress.SetStage("DM history")
	progress.AddUnits(len(pollDMs))
	dmResults, err := c.fanOutHistorySince(ctx, token, pollDMs, since, now, opts, directive, progress)
	if err != nil {
		if ctx.Err() != nil {
			return finalize(), nil
		}
		return nil, err
	}
	for i, ch := range pollDMs {
		for _, m := range dmResults[i] {
			if m.User == userID {
				continue // user's own messages are reported via "sent" path
			}
			add(collected{
				message: m, channel: ch, kind: "slack_dm", isSelf: true,
			})
		}
	}
	if ctx.Err() != nil {
		return finalize(), nil
	}

	progress.SetStage("mentions")
	mentionMatches, err := c.fetchSearchMessages(ctx, token, "<@"+userID+"> after:"+slackAfterDate(since), opts, directive.ID)
	if err != nil {
		if ctx.Err() != nil {
			return finalize(), nil
		}
		return nil, fmt.Errorf("slack search.messages (mentions): %w", err)
	}
	for _, m := range mentionMatches {
		t, err := slackTSToTime(m.TS)
		if err != nil || t.Before(since) || t.After(now) {
			continue
		}
		ch := channelFromSearchMatch(m)
		channelCache.put(ch)
		add(collected{
			message: searchMatchToMessage(m), channel: ch,
			kind: "slack_mention", isSelf: true,
		})
	}

	if ctx.Err() != nil {
		return finalize(), nil
	}
	progress.SetStage("sent")
	sentMatches, err := c.fetchSearchMessages(ctx, token, "from:<@"+userID+"> after:"+slackAfterDate(since), opts, directive.ID)
	if err != nil {
		if ctx.Err() != nil {
			return finalize(), nil
		}
		return nil, fmt.Errorf("slack search.messages (sent): %w", err)
	}
	type selfMessageRef struct {
		channel slackChannel
		ts      string
		thread  string
	}
	var selfRefs []selfMessageRef
	for _, m := range sentMatches {
		t, err := slackTSToTime(m.TS)
		if err != nil || t.Before(since) || t.After(now) {
			continue
		}
		ch := channelFromSearchMatch(m)
		channelCache.put(ch)
		msg := searchMatchToMessage(m)
		add(collected{
			message: msg, channel: ch, kind: "slack_sent", isSelf: true,
		})
		selfRefs = append(selfRefs, selfMessageRef{
			channel: ch, ts: m.TS, thread: msg.ThreadTS,
		})
	}

	if ctx.Err() != nil {
		return finalize(), nil
	}

	// --- involved-tier signals ---

	if scope == ScopeInvolved || scope == ScopeAll {
		// Threads the user is participating in: every distinct
		// (channel, thread_ts) pair from the sent-messages set.
		type threadKey struct{ channel, ts string }
		seenThreads := map[threadKey]struct{}{}
		// Pre-count unique thread fetches so the bar denominator
		// grows once (in AddUnits) rather than on every iteration.
		uniqueThreads := 0
		for _, ref := range selfRefs {
			ts := ref.thread
			if ts == "" {
				ts = ref.ts
			}
			tk := threadKey{channel: ref.channel.ID, ts: ts}
			if _, ok := seenThreads[tk]; !ok {
				seenThreads[tk] = struct{}{}
				uniqueThreads++
			}
		}
		// Reset for the actual iteration below.
		seenThreads = map[threadKey]struct{}{}
		progress.SetStage("threads")
		progress.AddUnits(uniqueThreads)
		for _, ref := range selfRefs {
			ts := ref.thread
			if ts == "" {
				ts = ref.ts
			}
			tk := threadKey{channel: ref.channel.ID, ts: ts}
			if _, ok := seenThreads[tk]; ok {
				continue
			}
			seenThreads[tk] = struct{}{}
			replies, err := c.fetchThreadReplies(ctx, token, ref.channel.ID, ts, opts, directive.ID)
			progress.Done()
			if err != nil {
				if isTolerablePerChannelError(err) {
					loggerFor(opts, directive.ID).Note(
						"slack: skipping thread replies for %s/%s: %v", ref.channel.ID, ts, err,
					)
					continue
				}
				if ctx.Err() != nil {
					return finalize(), nil
				}
				return nil, fmt.Errorf("slack conversations.replies %s/%s: %w", ref.channel.ID, ts, err)
			}
			for _, rm := range replies {
				rt, err := slackTSToTime(rm.TS)
				if err != nil || rt.Before(since) || rt.After(now) {
					continue
				}
				kind := "slack_thread_reply"
				isSelf := rm.User == userID
				add(collected{
					message: rm, channel: ref.channel, kind: kind, isSelf: isSelf,
					fields: map[string]string{"thread_ts": ts},
				})
			}
		}

		if ctx.Err() != nil {
			return finalize(), nil
		}

		// Context window: 3 messages before and after each self message.
		progress.SetStage("context windows")
		progress.AddUnits(len(selfRefs))
		for _, ref := range selfRefs {
			ctxMsgs, err := c.fetchContextWindow(ctx, token, ref.channel.ID, ref.ts, opts, directive.ID)
			progress.Done()
			if err != nil {
				if isTolerablePerChannelError(err) {
					loggerFor(opts, directive.ID).Note(
						"slack: skipping context window for %s/%s: %v", ref.channel.ID, ref.ts, err,
					)
					continue
				}
				if ctx.Err() != nil {
					return finalize(), nil
				}
				return nil, fmt.Errorf("slack context window %s/%s: %w", ref.channel.ID, ref.ts, err)
			}
			for _, cm := range ctxMsgs {
				ct, err := slackTSToTime(cm.TS)
				if err != nil || ct.Before(since) || ct.After(now) {
					continue
				}
				add(collected{
					message: cm, channel: ref.channel, kind: "slack_context",
					isSelf: cm.User == userID,
					fields: map[string]string{"context_for": ref.ts},
				})
			}
		}
	}

	if ctx.Err() != nil {
		return finalize(), nil
	}

	// --- all-tier signals ---

	if scope == ScopeAll {
		followed := parseFollowedList(directive.Config["followed_channels"])
		if len(followed) > 0 {
			named := []string{}
			for _, entry := range followed {
				e := strings.TrimPrefix(strings.TrimSpace(entry), "#")
				if isSlackChannelID(e) {
					if _, ok := channelCache.byID(e); !ok {
						channelCache.put(slackChannel{ID: e, Name: e, IsChannel: true})
					}
					named = append(named, e)
					continue
				}
				if ch, ok := channelCache.byName(e); ok {
					named = append(named, ch.ID)
					continue
				}
				// We need the public/private channel directory to map names.
				// Lazily fetch on first miss.
				if _, err := c.fetchAllConversations(ctx, token, "public_channel,private_channel", channelCache, opts, directive.ID); err != nil {
					if ctx.Err() != nil {
						return finalize(), nil
					}
					return nil, fmt.Errorf("slack conversations.list (followed): %w", err)
				}
				if ch, ok := channelCache.byName(e); ok {
					named = append(named, ch.ID)
				}
			}
			followedChans := make([]slackChannel, 0, len(named))
			for _, chID := range named {
				ch, ok := channelCache.byID(chID)
				if !ok {
					ch = slackChannel{ID: chID, Name: chID}
				}
				followedChans = append(followedChans, ch)
			}
			progress.SetStage("followed channels")
			progress.AddUnits(len(followedChans))
			followedResults, err := c.fanOutHistorySince(ctx, token, followedChans, since, now, opts, directive, progress)
			if err != nil {
				if ctx.Err() != nil {
					return finalize(), nil
				}
				return nil, err
			}
			for i, ch := range followedChans {
				for _, m := range followedResults[i] {
					add(collected{
						message: m, channel: ch,
						kind:   "slack_channel_message",
						isSelf: m.User == userID,
					})
				}
			}
		}
	}

	progress.SetStage("authors")
	return finalize(), nil
}

// ValidateDirective resolves the configured token, verifies it via auth.test,
// and warns when the token is a bot token (bot tokens cannot read DMs or
// run from:@me searches the collector relies on).
func (c SlackCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	if tokenKey == "" {
		return []ValidationIssue{{
			Field:       "credential_refs.token",
			Message:     "no Slack token configured",
			Remediation: "add credential_refs.token pointing at the env var holding your Slack user OAuth token (xoxp-...)",
		}}
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	token := userdata.ResolveEnv(userdataDir, tokenKey)
	if token == "" {
		return []ValidationIssue{{
			Field:       "credential_refs.token",
			Message:     fmt.Sprintf("Slack token env %q is empty", tokenKey),
			Remediation: fmt.Sprintf("set %s in your environment or in %s/.env to a Slack user OAuth token (xoxp-...)", tokenKey, userdataDir),
		}}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	auth, err := c.callAuthTest(probeCtx, token, nil, directive.ID)
	if err != nil {
		return []ValidationIssue{{
			Field:       "auth",
			Message:     fmt.Sprintf("Slack auth.test failed: %v", err),
			Remediation: slackTokenScopesRemediation,
		}}
	}
	if strings.TrimSpace(auth.BotID) != "" {
		return []ValidationIssue{{
			Field:       "credential_refs.token",
			Message:     "Slack token is a bot token; the collector needs a user OAuth token (xoxp-...) to read DMs and from:@me searches",
			Remediation: "create a Slack app, add user-token scopes, install for the workspace, and use the resulting xoxp- token. " + slackTokenScopesRemediation,
		}}
	}
	return nil
}

// callAuthTest performs the Slack auth.test handshake. It is reused by both
// Validate and Collect.
func (c SlackCollector) callAuthTest(ctx context.Context, token string, opts *CollectOpts, directiveID string) (slackAuthTest, error) {
	var out slackAuthTest
	if err := c.callAPI(ctx, token, http.MethodGet, "auth.test", nil, &out, opts, directiveID); err != nil {
		return out, err
	}
	return out, nil
}

func (c SlackCollector) fetchAllConversations(ctx context.Context, token, types string, cache *slackChannelCache, opts *CollectOpts, directiveID string) ([]slackChannel, error) {
	var out []slackChannel
	cursor := ""
	for {
		params := url.Values{}
		params.Set("types", types)
		params.Set("limit", "200")
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp slackConversationsList
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.list", params, &resp, opts, directiveID); err != nil {
			return nil, err
		}
		for _, ch := range resp.Channels {
			cache.put(ch)
			out = append(out, ch)
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			return out, nil
		}
	}
}

// fanOutHistorySince fetches conversations.history for each channel in
// `channels` using a bounded worker pool. Results[i] always corresponds to
// channels[i]; an empty slice means the channel had no messages in the
// window. The first error wins: on failure, the context is cancelled so
// the remaining workers unblock quickly and we don't keep talking to a
// rate-limited Slack tenant.
//
// Concurrency defaults to defaultHistoryConcurrency, overridable per
// directive via Config["history_concurrency"] (clamped to [1,
// maxHistoryConcurrency] so a typo cannot DoS the workspace).
//
// progress is the per-directive bar tracker; when non-nil, each
// finished (or tolerably-failed) channel ticks one unit of completion
// so the user sees a live count of "channels processed". Pass nil
// when running without progress wiring (e.g. tests).
func (c SlackCollector) fanOutHistorySince(ctx context.Context, token string, channels []slackChannel, since, now time.Time, opts *CollectOpts, directive userdata.Directive, progress *slackProgress) ([][]slackMessage, error) {
	if len(channels) == 0 {
		return nil, nil
	}
	results := make([][]slackMessage, len(channels))
	workers := historyConcurrency(directive)
	if workers > len(channels) {
		workers = len(channels)
	}

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type job struct {
		index   int
		channel slackChannel
	}
	jobs := make(chan job)
	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		firstEr error
	)
	recordErr := func(err error) {
		errOnce.Do(func() {
			firstEr = err
			cancel()
		})
	}

	logger := loggerFor(opts, directive.ID)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if jobCtx.Err() != nil {
					return
				}
				msgs, err := c.fetchHistorySince(jobCtx, token, j.channel.ID, since, now, opts, directive.ID)
				if err != nil {
					// Tolerable per-channel errors (channel_not_found,
					// not_in_channel, ...) mean "this one channel is
					// unreachable" — log it and keep going so a single
					// stale DM in conversations.list doesn't blow up
					// the whole run.
					if isTolerablePerChannelError(err) {
						logger.Note("slack: skipping channel %s: %v", j.channel.ID, err)
						progress.Done()
						continue
					}
					// Suppress context-cancelled noise: that's just
					// "another worker hit an error and we bailed".
					if jobCtx.Err() == nil {
						recordErr(fmt.Errorf("slack conversations.history %s: %w", j.channel.ID, err))
					}
					return
				}
				results[j.index] = msgs
				progress.Done()
			}
		}()
	}

dispatch:
	for i, ch := range channels {
		select {
		case <-jobCtx.Done():
			break dispatch
		case jobs <- job{index: i, channel: ch}:
		}
	}
	close(jobs)
	wg.Wait()
	if firstEr != nil {
		return nil, firstEr
	}
	return results, nil
}

// historyConcurrency reads Config["history_concurrency"] and clamps it to
// the supported range. Missing/empty/zero values fall back to the default.
func historyConcurrency(directive userdata.Directive) int {
	raw := strings.TrimSpace(directive.Config["history_concurrency"])
	if raw == "" {
		return defaultHistoryConcurrency
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultHistoryConcurrency
	}
	if n > maxHistoryConcurrency {
		return maxHistoryConcurrency
	}
	return n
}

// dmDiscoveryEnabled reads Config["dm_discovery"]. It defaults to enabled
// ("auto"): use a to:<@me> search to find active DMs and only poll those,
// falling back to a full fan-out if search is unavailable. Set to
// "off"/"false"/"no"/"0" to always poll every DM (the original behavior).
func dmDiscoveryEnabled(directive userdata.Directive) bool {
	switch strings.ToLower(strings.TrimSpace(directive.Config["dm_discovery"])) {
	case "off", "false", "no", "0":
		return false
	default:
		return true
	}
}

func (c SlackCollector) fetchHistorySince(ctx context.Context, token, channelID string, since, now time.Time, opts *CollectOpts, directiveID string) ([]slackMessage, error) {
	var out []slackMessage
	cursor := ""
	for {
		params := url.Values{}
		params.Set("channel", channelID)
		params.Set("oldest", slackOldest(since))
		params.Set("inclusive", "true")
		params.Set("limit", "200")
		if !now.IsZero() {
			params.Set("latest", slackOldest(now))
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp slackHistory
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp, opts, directiveID); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
			if m.Type != "" && m.Type != "message" {
				continue
			}
			out = append(out, m)
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			return out, nil
		}
	}
}

func (c SlackCollector) fetchThreadReplies(ctx context.Context, token, channelID, threadTS string, opts *CollectOpts, directiveID string) ([]slackMessage, error) {
	var out []slackMessage
	cursor := ""
	for {
		params := url.Values{}
		params.Set("channel", channelID)
		params.Set("ts", threadTS)
		params.Set("limit", "200")
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp slackReplies
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.replies", params, &resp, opts, directiveID); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
			if m.Type != "" && m.Type != "message" {
				continue
			}
			out = append(out, m)
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			return out, nil
		}
	}
}

// fetchContextWindow returns up to 3 messages before and 3 messages after ts
// within the same channel, using two non-inclusive conversations.history
// calls. Slack requires `latest` for the "before" half (older=true).
func (c SlackCollector) fetchContextWindow(ctx context.Context, token, channelID, ts string, opts *CollectOpts, directiveID string) ([]slackMessage, error) {
	before, err := func() ([]slackMessage, error) {
		params := url.Values{}
		params.Set("channel", channelID)
		params.Set("latest", ts)
		params.Set("inclusive", "false")
		params.Set("limit", "3")
		var resp slackHistory
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp, opts, directiveID); err != nil {
			return nil, err
		}
		return resp.Messages, nil
	}()
	if err != nil {
		return nil, err
	}
	after, err := func() ([]slackMessage, error) {
		params := url.Values{}
		params.Set("channel", channelID)
		params.Set("oldest", ts)
		params.Set("inclusive", "false")
		params.Set("limit", "3")
		var resp slackHistory
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp, opts, directiveID); err != nil {
			return nil, err
		}
		return resp.Messages, nil
	}()
	if err != nil {
		return nil, err
	}
	out := make([]slackMessage, 0, len(before)+len(after))
	for _, m := range before {
		if m.Type != "" && m.Type != "message" {
			continue
		}
		out = append(out, m)
	}
	for _, m := range after {
		if m.Type != "" && m.Type != "message" {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (c SlackCollector) fetchSearchMessages(ctx context.Context, token, query string, opts *CollectOpts, directiveID string) ([]slackSearchMatch, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("count", "100")
	params.Set("sort", "timestamp")
	params.Set("sort_dir", "desc")
	var resp slackSearchMessages
	if err := c.callAPI(ctx, token, http.MethodGet, "search.messages", params, &resp, opts, directiveID); err != nil {
		return nil, err
	}
	return resp.Messages.Matches, nil
}

// selectDMsToPoll returns the subset of activeDMs that should be queried
// with conversations.history. With discovery enabled (the default), it
// runs a to:<@me> search to find 1:1 DMs with inbound messages and polls
// only those plus all MPIMs; otherwise (or on any search failure) it
// returns every active DM so no message is silently dropped.
func (c SlackCollector) selectDMsToPoll(ctx context.Context, token, userID string, since time.Time, activeDMs []slackChannel, opts *CollectOpts, directive userdata.Directive, progress *slackProgress) []slackChannel {
	if !dmDiscoveryEnabled(directive) || len(activeDMs) == 0 {
		return activeDMs
	}
	logger := loggerFor(opts, directive.ID)

	// MPIMs (and any non-IM) are always polled: `to:` searches only
	// surface 1:1 IMs, so we can't discover MPIM activity this way.
	var ims, mpims []slackChannel
	for _, ch := range activeDMs {
		if ch.IsIM {
			ims = append(ims, ch)
		} else {
			mpims = append(mpims, ch)
		}
	}

	progress.SetStage("discovering DMs")
	active, capHit, err := c.discoverActiveIMChannels(ctx, token, userID, since, opts, directive.ID)
	if err != nil {
		logger.Note("slack: DM discovery search unavailable (%v); polling all %d DM(s)", err, len(activeDMs))
		return activeDMs
	}
	if capHit {
		logger.Note("slack: DM discovery exceeded %d pages; polling all %d DM(s)", slackDiscoveryMaxPages, len(activeDMs))
		return activeDMs
	}

	poll := make([]slackChannel, 0, len(mpims)+len(active))
	seen := map[string]struct{}{}
	for _, ch := range mpims {
		poll = append(poll, ch)
		seen[ch.ID] = struct{}{}
	}
	for _, ch := range ims {
		if _, ok := active[ch.ID]; ok {
			poll = append(poll, ch)
			seen[ch.ID] = struct{}{}
		}
	}
	// Defensive: poll any discovered IM channel that conversations.list
	// didn't return (e.g. a listing race), so discovery never drops one.
	for id := range active {
		if _, ok := seen[id]; ok {
			continue
		}
		if strings.HasPrefix(id, "D") {
			poll = append(poll, slackChannel{ID: id, IsIM: true})
			seen[id] = struct{}{}
		}
	}

	logger.Note(
		"slack: DM discovery polling %d of %d DM(s) (%d active 1:1 + %d MPIM) via to:<@me> search",
		len(poll), len(activeDMs), len(poll)-len(mpims), len(mpims),
	)
	return poll
}

// discoverActiveIMChannels runs a paginated `to:<@user> after:DATE`
// search to find the set of 1:1 DM (IM) channel IDs that have an inbound
// message in the window. This lets the caller poll conversations.history
// for only the handful of active DMs instead of fanning out across every
// DM the user can see (on long-lived accounts the overwhelming majority
// are dormant).
//
// Correctness notes:
//   - The search `after:` filter is day-granular, so we query from one
//     day before `since` to avoid dropping a channel whose only inbound
//     message lands on the boundary day. Over-discovery is harmless —
//     conversations.history re-applies the exact [oldest,latest] window.
//   - Search may collapse multiple nearby matches into one, but it still
//     surfaces each active channel at least once, which is all we need:
//     the authoritative messages come from conversations.history.
//   - `to:` searches only return 1:1 IMs (verified against the live API);
//     group DMs (MPIMs) never appear, so the caller polls MPIMs directly.
//
// The bool return is true when the page cap was hit (results are
// incomplete and the caller should fall back to a full fan-out). On any
// API error the error is returned so the caller can fall back too.
func (c SlackCollector) discoverActiveIMChannels(ctx context.Context, token, userID string, since time.Time, opts *CollectOpts, directiveID string) (map[string]struct{}, bool, error) {
	active := map[string]struct{}{}
	query := "to:<@" + userID + "> after:" + slackAfterDate(since.AddDate(0, 0, -1))
	for page := 1; ; page++ {
		if page > slackDiscoveryMaxPages {
			return active, true, nil
		}
		params := url.Values{}
		params.Set("query", query)
		params.Set("count", "100")
		params.Set("sort", "timestamp")
		params.Set("sort_dir", "desc")
		params.Set("page", strconv.Itoa(page))
		var resp slackSearchMessages
		if err := c.callAPI(ctx, token, http.MethodGet, "search.messages", params, &resp, opts, directiveID); err != nil {
			return nil, false, err
		}
		for _, m := range resp.Messages.Matches {
			if id := strings.TrimSpace(m.Channel.ID); id != "" {
				active[id] = struct{}{}
			}
		}
		pages := resp.Messages.Paging.Pages
		if pages <= 0 || page >= pages {
			return active, false, nil
		}
	}
}

func (c SlackCollector) fetchUser(ctx context.Context, token, userID string, opts *CollectOpts, directiveID string) (slackUser, error) {
	params := url.Values{}
	params.Set("user", userID)
	var resp slackUsersInfo
	if err := c.callAPI(ctx, token, http.MethodGet, "users.info", params, &resp, opts, directiveID); err != nil {
		return slackUser{}, err
	}
	return resp.User, nil
}

// callAPI issues a Slack Web API request and unmarshals into out. Slack
// returns a 200 response with `ok:false` and a textual `error` field on
// auth/scope failures, so we surface those as Go errors here.
//
// 429 responses are retried transparently up to slackMaxRetries times,
// honoring the Retry-After header (capped at slackMaxRetryAfter so a
// runaway value doesn't hang the run). When all retries are exhausted
// the final 429 is returned as an error like the original behavior.
func (c SlackCollector) callAPI(ctx context.Context, token, method, endpoint string, params url.Values, out any, opts *CollectOpts, directiveID string) error {
	full := c.baseURL() + "/" + strings.TrimLeft(endpoint, "/")
	if params != nil && method == http.MethodGet {
		full += "?" + params.Encode()
	}
	encodedBody := ""
	if method != http.MethodGet && params != nil {
		encodedBody = params.Encode()
	}

	var lastStatus string
	var lastSnippet string
	for attempt := 0; attempt <= slackMaxRetries; attempt++ {
		var body io.Reader
		if encodedBody != "" {
			body = strings.NewReader(encodedBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, full, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if method != http.MethodGet {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, raw, err := doAndReadHTTP(c.client(), req, 8<<20, opts, directiveID)
		if err != nil {
			return err
		}
		if res.StatusCode == http.StatusTooManyRequests {
			lastStatus = res.Status
			lastSnippet = trimSlackSnippet(raw)
			if attempt == slackMaxRetries {
				return fmt.Errorf("slack %s %s (after %d retries): %s", endpoint, res.Status, slackMaxRetries, lastSnippet)
			}
			wait := slackRetryAfter(res.Header.Get("Retry-After"), attempt)
			loggerFor(opts, directiveID).Note(
				"slack %s: rate limited, sleeping %s before retry (attempt %d/%d)",
				endpoint, wait, attempt+1, slackMaxRetries,
			)
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("slack %s %s: %s", endpoint, res.Status, trimSlackSnippet(raw))
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("parse slack %s: %w", endpoint, err)
		}
		// Every response wraps slackResponseMeta; reflect across the structure
		// by re-decoding into the meta type for the OK/error fields.
		var meta slackResponseMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("parse slack %s meta: %w", endpoint, err)
		}
		if !meta.OK {
			return &slackAPIError{Endpoint: endpoint, Code: strings.TrimSpace(meta.Error)}
		}
		return nil
	}
	// Unreachable: the loop either returns inside or retries on 429.
	return fmt.Errorf("slack %s %s: %s", endpoint, lastStatus, lastSnippet)
}

// slackRetryAfter parses Slack's Retry-After header (seconds), falling
// back to an exponential backoff when the header is absent or malformed.
// All values are clamped to [1s, slackMaxRetryAfter] so a single retry
// never blocks the run for an unbounded period.
func slackRetryAfter(header string, attempt int) time.Duration {
	if v := strings.TrimSpace(header); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > slackMaxRetryAfter {
				return slackMaxRetryAfter
			}
			return d
		}
	}
	// Exponential backoff: 1s, 2s, 4s, 8s ... capped.
	d := time.Second << attempt
	if d <= 0 || d > slackMaxRetryAfter {
		return slackMaxRetryAfter
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func trimSlackSnippet(raw []byte) string {
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return snippet
}

// --- helpers ---

// slackTSToTime converts a Slack timestamp string ("1700000000.123456") into
// a UTC time.Time. The fractional component encodes microseconds.
func slackTSToTime(ts string) (time.Time, error) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, fmt.Errorf("empty slack ts")
	}
	secStr, fracStr, ok := strings.Cut(ts, ".")
	if !ok {
		secStr = ts
		fracStr = "0"
	}
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse slack ts %q: %w", ts, err)
	}
	if fracStr == "" {
		fracStr = "0"
	}
	// Slack fractional component is microseconds (6 digits). Pad/truncate
	// before parsing so values like "1700000000.5" still produce a value.
	for len(fracStr) < 6 {
		fracStr += "0"
	}
	if len(fracStr) > 6 {
		fracStr = fracStr[:6]
	}
	micro, err := strconv.ParseInt(fracStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse slack ts fraction %q: %w", ts, err)
	}
	return time.Unix(sec, micro*1000).UTC(), nil
}

func slackOldest(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return fmt.Sprintf("%d.000000", t.Unix())
}

func slackAfterDate(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01"
	}
	return t.UTC().Format("2006-01-02")
}

// slackPermalink mirrors the URL Slack would generate via chat.getPermalink
// without an extra round trip. teamURL must be the workspace base
// ("https://acme.slack.com/" from auth.test).
func slackPermalink(teamURL, channelID, ts string) string {
	teamURL = strings.TrimRight(strings.TrimSpace(teamURL), "/")
	channelID = strings.TrimSpace(channelID)
	ts = strings.TrimSpace(ts)
	if teamURL == "" || channelID == "" || ts == "" {
		return ""
	}
	tsClean := strings.ReplaceAll(ts, ".", "")
	return fmt.Sprintf("%s/archives/%s/p%s", teamURL, channelID, tsClean)
}

func slackChannelKey(ch slackChannel, users *slackUserCache, selfID string) string {
	if ch.IsIM {
		peer := ch.User
		if peer == "" || peer == selfID {
			return "dm:" + ch.ID
		}
		if u, ok := users.get(peer); ok {
			if name := slackUserDisplay(u); name != "" {
				return "dm:" + name
			}
		}
		return "dm:" + peer
	}
	if ch.IsMPIM {
		if name := strings.TrimSpace(ch.Name); name != "" {
			return "mpim:" + name
		}
		return "mpim:" + ch.ID
	}
	if name := strings.TrimSpace(ch.Name); name != "" {
		return "#" + name
	}
	return ch.ID
}

func slackUserDisplay(u slackUser) string {
	if v := strings.TrimSpace(u.Profile.DisplayName); v != "" {
		return v
	}
	if v := strings.TrimSpace(u.Profile.RealName); v != "" {
		return v
	}
	if v := strings.TrimSpace(u.RealName); v != "" {
		return v
	}
	return strings.TrimSpace(u.Name)
}

func slackAuthorDisplay(m slackMessage, users *slackUserCache) string {
	if m.User != "" {
		if u, ok := users.get(m.User); ok {
			if d := slackUserDisplay(u); d != "" {
				return d
			}
		}
		return m.User
	}
	if m.Username != "" {
		return m.Username
	}
	if m.BotID != "" {
		return "bot:" + m.BotID
	}
	return ""
}

func slackTruncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	if cut > len(s) {
		cut = len(s)
	}
	return s[:cut] + "..."
}

func isSlackChannelID(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[0] {
	case 'C', 'G', 'D':
		// Channel IDs are uppercase alphanumeric after the prefix.
		for _, r := range s[1:] {
			if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z')) {
				return false
			}
		}
		return true
	}
	return false
}

// slackKindRank ranks Slack signal kinds so dedupe keeps the most specific.
// Higher is more specific (slack_mention beats slack_channel_message).
func slackKindRank(kind string) int {
	switch kind {
	case "slack_mention":
		return 50
	case "slack_dm":
		return 45
	case "slack_sent":
		return 40
	case "slack_thread_reply":
		return 30
	case "slack_channel_message":
		return 20
	case "slack_context":
		return 10
	default:
		return 0
	}
}

// channelFromSearchMatch builds the minimal slackChannel record we can keep
// from a search.messages hit (sufficient for permalink + grouping).
func channelFromSearchMatch(m slackSearchMatch) slackChannel {
	return slackChannel{
		ID:        m.Channel.ID,
		Name:      m.Channel.Name,
		IsChannel: true,
	}
}

func searchMatchToMessage(m slackSearchMatch) slackMessage {
	return slackMessage{
		Type:     "message",
		User:     m.User,
		Username: m.Username,
		Text:     m.Text,
		TS:       m.TS,
		Team:     m.Team,
	}
}

// --- caches ---

type slackChannelCache struct {
	byIDMap   map[string]slackChannel
	byNameMap map[string]slackChannel
}

func newSlackChannelCache() *slackChannelCache {
	return &slackChannelCache{
		byIDMap:   map[string]slackChannel{},
		byNameMap: map[string]slackChannel{},
	}
}

func (c *slackChannelCache) put(ch slackChannel) {
	if ch.ID == "" {
		return
	}
	c.byIDMap[ch.ID] = ch
	if name := strings.TrimSpace(ch.Name); name != "" {
		c.byNameMap[strings.ToLower(name)] = ch
	}
}

func (c *slackChannelCache) byID(id string) (slackChannel, bool) {
	ch, ok := c.byIDMap[id]
	return ch, ok
}

func (c *slackChannelCache) byName(name string) (slackChannel, bool) {
	ch, ok := c.byNameMap[strings.ToLower(strings.TrimSpace(name))]
	return ch, ok
}

type slackUserCache struct {
	users map[string]slackUser
}

func newSlackUserCache() *slackUserCache {
	return &slackUserCache{users: map[string]slackUser{}}
}

func (c *slackUserCache) put(u slackUser) {
	if u.ID == "" {
		return
	}
	c.users[u.ID] = u
}

func (c *slackUserCache) get(id string) (slackUser, bool) {
	u, ok := c.users[id]
	return u, ok
}
