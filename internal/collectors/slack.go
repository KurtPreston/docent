package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
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
	ID                 string `json:"id"`
	Name               string `json:"name,omitempty"`
	IsIM               bool   `json:"is_im,omitempty"`
	IsMPIM             bool   `json:"is_mpim,omitempty"`
	IsGroup            bool   `json:"is_group,omitempty"`
	IsPrivate          bool   `json:"is_private,omitempty"`
	IsChannel          bool   `json:"is_channel,omitempty"`
	User               string `json:"user,omitempty"` // peer for is_im
	Members            int    `json:"num_members,omitempty"`
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
			Pages int `json:"pages,omitempty"`
		} `json:"paging,omitempty"`
	} `json:"messages"`
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
func (c SlackCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	if tokenKey == "" {
		return nil, fmt.Errorf("slack credential missing (set credential_refs.token to a SLAKKR_SLACK_TOKEN-style env var)")
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

	auth, err := c.callAuthTest(ctx, token)
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

	// --- self-tier signals (always run) ---

	dmChannels, err := c.fetchAllConversations(ctx, token, "im,mpim", channelCache)
	if err != nil {
		return nil, fmt.Errorf("slack conversations.list (dm): %w", err)
	}
	for _, ch := range dmChannels {
		msgs, err := c.fetchHistorySince(ctx, token, ch.ID, since, now)
		if err != nil {
			return nil, fmt.Errorf("slack conversations.history %s: %w", ch.ID, err)
		}
		for _, m := range msgs {
			if m.User == userID {
				continue // user's own messages are reported via "sent" path
			}
			add(collected{
				message: m, channel: ch, kind: "slack_dm", isSelf: true,
			})
		}
	}

	mentionMatches, err := c.fetchSearchMessages(ctx, token, "<@"+userID+"> after:"+slackAfterDate(since))
	if err != nil {
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

	sentMatches, err := c.fetchSearchMessages(ctx, token, "from:<@"+userID+"> after:"+slackAfterDate(since))
	if err != nil {
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

	// --- involved-tier signals ---

	if scope == ScopeInvolved || scope == ScopeAll {
		// Threads the user is participating in: every distinct
		// (channel, thread_ts) pair from the sent-messages set.
		type threadKey struct{ channel, ts string }
		seenThreads := map[threadKey]struct{}{}
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
			replies, err := c.fetchThreadReplies(ctx, token, ref.channel.ID, ts)
			if err != nil {
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

		// Context window: 3 messages before and after each self message.
		for _, ref := range selfRefs {
			ctxMsgs, err := c.fetchContextWindow(ctx, token, ref.channel.ID, ref.ts)
			if err != nil {
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
				if _, err := c.fetchAllConversations(ctx, token, "public_channel,private_channel", channelCache); err != nil {
					return nil, fmt.Errorf("slack conversations.list (followed): %w", err)
				}
				if ch, ok := channelCache.byName(e); ok {
					named = append(named, ch.ID)
				}
			}
			for _, chID := range named {
				ch, ok := channelCache.byID(chID)
				if !ok {
					ch = slackChannel{ID: chID, Name: chID}
				}
				msgs, err := c.fetchHistorySince(ctx, token, chID, since, now)
				if err != nil {
					return nil, fmt.Errorf("slack conversations.history %s: %w", chID, err)
				}
				for _, m := range msgs {
					add(collected{
						message: m, channel: ch,
						kind:   "slack_channel_message",
						isSelf: m.User == userID,
					})
				}
			}
		}
	}

	// Resolve unique authors so StatusItem.Author renders as a name when
	// possible, falling back to the bare user ID.
	for _, b := range bucket {
		if b.message.User == "" {
			continue
		}
		if _, ok := userCache.get(b.message.User); ok {
			continue
		}
		u, err := c.fetchUser(ctx, token, b.message.User)
		if err != nil {
			// Best-effort: skip on lookup failure (rate limit, missing scope).
			userCache.put(slackUser{ID: b.message.User})
			continue
		}
		userCache.put(u)
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
	return items, nil
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
	auth, err := c.callAuthTest(probeCtx, token)
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
func (c SlackCollector) callAuthTest(ctx context.Context, token string) (slackAuthTest, error) {
	var out slackAuthTest
	if err := c.callAPI(ctx, token, http.MethodGet, "auth.test", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c SlackCollector) fetchAllConversations(ctx context.Context, token, types string, cache *slackChannelCache) ([]slackChannel, error) {
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
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.list", params, &resp); err != nil {
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

func (c SlackCollector) fetchHistorySince(ctx context.Context, token, channelID string, since, now time.Time) ([]slackMessage, error) {
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
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp); err != nil {
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

func (c SlackCollector) fetchThreadReplies(ctx context.Context, token, channelID, threadTS string) ([]slackMessage, error) {
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
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.replies", params, &resp); err != nil {
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
func (c SlackCollector) fetchContextWindow(ctx context.Context, token, channelID, ts string) ([]slackMessage, error) {
	before, err := func() ([]slackMessage, error) {
		params := url.Values{}
		params.Set("channel", channelID)
		params.Set("latest", ts)
		params.Set("inclusive", "false")
		params.Set("limit", "3")
		var resp slackHistory
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp); err != nil {
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
		if err := c.callAPI(ctx, token, http.MethodGet, "conversations.history", params, &resp); err != nil {
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

func (c SlackCollector) fetchSearchMessages(ctx context.Context, token, query string) ([]slackSearchMatch, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("count", "100")
	params.Set("sort", "timestamp")
	params.Set("sort_dir", "desc")
	var resp slackSearchMessages
	if err := c.callAPI(ctx, token, http.MethodGet, "search.messages", params, &resp); err != nil {
		return nil, err
	}
	return resp.Messages.Matches, nil
}

func (c SlackCollector) fetchUser(ctx context.Context, token, userID string) (slackUser, error) {
	params := url.Values{}
	params.Set("user", userID)
	var resp slackUsersInfo
	if err := c.callAPI(ctx, token, http.MethodGet, "users.info", params, &resp); err != nil {
		return slackUser{}, err
	}
	return resp.User, nil
}

// callAPI issues a Slack Web API request and unmarshals into out. Slack
// returns a 200 response with `ok:false` and a textual `error` field on
// auth/scope failures, so we surface those as Go errors here.
func (c SlackCollector) callAPI(ctx context.Context, token, method, endpoint string, params url.Values, out any) error {
	full := c.baseURL() + "/" + strings.TrimLeft(endpoint, "/")
	if params != nil && method == http.MethodGet {
		full += "?" + params.Encode()
	}
	var body io.Reader
	if method != http.MethodGet && params != nil {
		body = strings.NewReader(params.Encode())
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
	res, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return fmt.Errorf("slack %s %s: %s", endpoint, res.Status, snippet)
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
		return fmt.Errorf("slack %s: %s", endpoint, strings.TrimSpace(meta.Error))
	}
	return nil
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
