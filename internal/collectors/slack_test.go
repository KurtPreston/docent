package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

// slackTestRequest captures one inbound API call so tests can assert what
// the collector requested.
type slackTestRequest struct {
	Method string
	Path   string
	Query  url.Values
	Token  string
}

// slackTestState routes each request through a per-test handler and keeps
// an ordered log for assertions.
type slackTestState struct {
	mu       sync.Mutex
	requests []slackTestRequest
	handler  func(req slackTestRequest) (int, any)
}

func (s *slackTestState) snapshot() []slackTestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]slackTestRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func newSlackServer(t *testing.T, handler func(req slackTestRequest) (int, any)) (*httptest.Server, *slackTestState) {
	t.Helper()
	state := &slackTestState{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		req := slackTestRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Token:  token,
		}
		state.mu.Lock()
		state.requests = append(state.requests, req)
		state.mu.Unlock()
		status, body := state.handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body == nil {
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

func newSlackDirective() userdata.Directive {
	return userdata.Directive{
		ID:        "slack",
		Name:      "Slack",
		Collector: "slack",
		Enabled:   true,
		Config:    map[string]string{},
		CredentialRefs: map[string]string{
			"token": "SLAKKR_SLACK_TEST_TOKEN",
		},
	}
}

func slackOK(payload map[string]any) map[string]any {
	out := map[string]any{"ok": true}
	for k, v := range payload {
		out[k] = v
	}
	return out
}

// authTestPayload returns a canonical successful auth.test response.
func authTestPayload(userID string) map[string]any {
	return slackOK(map[string]any{
		"url":     "https://acme.slack.com/",
		"team":    "Acme",
		"user":    "alice",
		"team_id": "T0001",
		"user_id": userID,
	})
}

// pathFor extracts the trailing API method name (e.g. "auth.test") from a
// /api/<method> request path.
func pathFor(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return p
	}
	return p[idx+1:]
}

func TestSlackTSToTime(t *testing.T) {
	cases := []struct {
		ts      string
		want    time.Time
		wantErr bool
	}{
		{ts: "1700000000.000000", want: time.Unix(1700000000, 0).UTC()},
		{ts: "1700000000.500000", want: time.Unix(1700000000, 500000*1000).UTC()},
		{ts: "1700000000", want: time.Unix(1700000000, 0).UTC()},
		{ts: "", wantErr: true},
		{ts: "not-a-number", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.ts, func(t *testing.T) {
			got, err := slackTSToTime(tc.ts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.ts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestSlackPermalink(t *testing.T) {
	got := slackPermalink("https://acme.slack.com/", "C12345", "1700000000.123456")
	want := "https://acme.slack.com/archives/C12345/p1700000000123456"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if slackPermalink("", "C1", "1.0") != "" {
		t.Errorf("expected empty result with empty teamURL")
	}
}

func TestIsSlackChannelID(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"#team":    false,
		"team":     false,
		"C12345":   true,
		"G0AB":     true,
		"DXYZ":     true,
		"c12345":   false, // case-sensitive prefix
		"C 12345":  false,
	}
	for in, want := range cases {
		if got := isSlackChannelID(in); got != want {
			t.Errorf("isSlackChannelID(%q) = %v want %v", in, got, want)
		}
	}
}

func TestSlackValidateDirectiveMissingToken(t *testing.T) {
	c := SlackCollector{Clock: time.Now}
	d := userdata.Directive{ID: "slack", Collector: "slack", Enabled: true}
	issues := c.ValidateDirective(context.Background(), d, &ValidateOpts{})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "no Slack token") {
		t.Fatalf("expected missing-token issue, got %+v", issues)
	}
}

func TestSlackValidateDirectiveEmptyEnv(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "")
	c := SlackCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), newSlackDirective(), &ValidateOpts{})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "is empty") {
		t.Fatalf("expected empty-env issue, got %+v", issues)
	}
}

func TestSlackValidateDirectiveInvalidAuth(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-bad")
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		return http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"}
	})
	c := SlackCollector{Clock: time.Now, BaseURL: srv.URL}
	issues := c.ValidateDirective(context.Background(), newSlackDirective(), &ValidateOpts{})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "invalid_auth") {
		t.Fatalf("expected invalid_auth issue, got %+v", issues)
	}
}

func TestSlackValidateDirectiveBotTokenWarn(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxb-bot")
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		return http.StatusOK, slackOK(map[string]any{
			"url":     "https://acme.slack.com/",
			"team":    "Acme",
			"user":    "bot",
			"team_id": "T0001",
			"user_id": "U_BOT",
			"bot_id":  "B12345",
		})
	})
	c := SlackCollector{Clock: time.Now, BaseURL: srv.URL}
	issues := c.ValidateDirective(context.Background(), newSlackDirective(), &ValidateOpts{})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "bot token") {
		t.Fatalf("expected bot-token warning, got %+v", issues)
	}
}

func TestSlackValidateDirectiveOK(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		return http.StatusOK, authTestPayload("U_SELF")
	})
	c := SlackCollector{Clock: time.Now, BaseURL: srv.URL}
	if issues := c.ValidateDirective(context.Background(), newSlackDirective(), &ValidateOpts{}); len(issues) != 0 {
		t.Fatalf("expected no issues, got %+v", issues)
	}
}

func TestSlackCollectScopeSelf(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{
				"channels": []map[string]any{
					{"id": "D_DM1", "is_im": true, "user": "U_PEER"},
				},
			})
		case "conversations.history":
			return http.StatusOK, slackOK(map[string]any{
				"messages": []map[string]any{
					{"type": "message", "user": "U_PEER", "text": "hi alice", "ts": "1778500000.100000"},
					{"type": "message", "user": userID, "text": "hi back", "ts": "1778500010.000000"},
				},
			})
		case "search.messages":
			query := req.Query.Get("query")
			if strings.Contains(query, "from:<@"+userID+">") {
				return http.StatusOK, slackOK(map[string]any{
					"messages": map[string]any{
						"matches": []map[string]any{
							{
								"type": "message", "user": userID,
								"text": "I sent this", "ts": "1778500020.000000",
								"channel": map[string]any{"id": "C_TEAM", "name": "team-foo"},
							},
						},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{
				"messages": map[string]any{
					"matches": []map[string]any{
						{
							"type": "message", "user": "U_OTHER",
							"text": "hey <@" + userID + ">", "ts": "1778500005.000000",
							"channel": map[string]any{"id": "C_TEAM", "name": "team-foo"},
						},
					},
				},
			})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.Kind]++
		if !it.IsSelf {
			t.Errorf("scope=self item should be IsSelf=true, got %+v", it)
		}
		if it.Source != "slack" {
			t.Errorf("expected Source=slack, got %q", it.Source)
		}
		if it.URL == "" {
			t.Errorf("expected non-empty permalink, got %+v", it)
		}
	}
	wants := map[string]int{
		"slack_dm":      1,
		"slack_mention": 1,
		"slack_sent":    1,
	}
	for kind, count := range wants {
		if got[kind] != count {
			t.Errorf("kind %q: got %d want %d (full: %+v)", kind, got[kind], count, items)
		}
	}
	if got["slack_thread_reply"] > 0 || got["slack_context"] > 0 || got["slack_channel_message"] > 0 {
		t.Errorf("scope=self should not include involved/all kinds: %+v", got)
	}
}

func TestSlackCollectScopeInvolvedAddsThreadAndContext(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"
	srv, state := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			// No DMs in this test.
			return http.StatusOK, slackOK(map[string]any{"channels": []map[string]any{}})
		case "search.messages":
			if strings.Contains(req.Query.Get("query"), "from:<@"+userID+">") {
				return http.StatusOK, slackOK(map[string]any{
					"messages": map[string]any{
						"matches": []map[string]any{
							{
								"type": "message", "user": userID,
								"text": "thread parent", "ts": "1778500050.000000",
								"channel": map[string]any{"id": "C_TEAM", "name": "team-foo"},
							},
						},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		case "conversations.replies":
			return http.StatusOK, slackOK(map[string]any{
				"messages": []map[string]any{
					{"type": "message", "user": userID, "text": "thread parent", "ts": "1778500050.000000"},
					{"type": "message", "user": "U_OTHER", "text": "reply by other", "ts": "1778500060.000000"},
				},
			})
		case "conversations.history":
			// Two calls expected: latest=ts (before) and oldest=ts (after).
			if req.Query.Get("latest") != "" {
				return http.StatusOK, slackOK(map[string]any{
					"messages": []map[string]any{
						{"type": "message", "user": "U_OTHER", "text": "before-1", "ts": "1778500040.000000"},
						{"type": "message", "user": "U_OTHER", "text": "before-2", "ts": "1778500045.000000"},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{
				"messages": []map[string]any{
					{"type": "message", "user": "U_OTHER", "text": "after-1", "ts": "1778500070.000000"},
				},
			})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeInvolved,
	})
	if err != nil {
		t.Fatal(err)
	}

	kinds := map[string]int{}
	var threadReplyByOther *StatusItem
	var contextByOther *StatusItem
	for i := range items {
		kinds[items[i].Kind]++
		if items[i].Kind == "slack_thread_reply" && !items[i].IsSelf {
			threadReplyByOther = &items[i]
		}
		if items[i].Kind == "slack_context" && !items[i].IsSelf {
			contextByOther = &items[i]
		}
	}
	if kinds["slack_sent"] != 1 {
		t.Errorf("expected 1 slack_sent (parent), got %d (kinds=%+v)", kinds["slack_sent"], kinds)
	}
	if threadReplyByOther == nil {
		t.Errorf("expected a non-self thread reply, got items=%+v", items)
	}
	if contextByOther == nil {
		t.Errorf("expected a non-self context message, got items=%+v", items)
	}

	// Verify the context window made the two non-inclusive calls.
	var sawLatest, sawOldest bool
	for _, r := range state.snapshot() {
		if pathFor(r.Path) != "conversations.history" {
			continue
		}
		if r.Query.Get("inclusive") == "false" && r.Query.Get("limit") == "3" {
			if r.Query.Get("latest") == "1778500050.000000" {
				sawLatest = true
			}
			if r.Query.Get("oldest") == "1778500050.000000" {
				sawOldest = true
			}
		}
	}
	if !sawLatest || !sawOldest {
		t.Errorf("expected non-inclusive context-window calls (before+after); sawLatest=%v sawOldest=%v", sawLatest, sawOldest)
	}
}

func TestSlackCollectScopeAllAddsFollowedChannels(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			types := req.Query.Get("types")
			switch types {
			case "im,mpim":
				return http.StatusOK, slackOK(map[string]any{"channels": []map[string]any{}})
			case "public_channel,private_channel":
				return http.StatusOK, slackOK(map[string]any{
					"channels": []map[string]any{
						{"id": "C_TEAM_X", "name": "team-x", "is_channel": true},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{"channels": []map[string]any{}})
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		case "conversations.history":
			if req.Query.Get("channel") == "C_TEAM_X" {
				return http.StatusOK, slackOK(map[string]any{
					"messages": []map[string]any{
						{"type": "message", "user": "U_OTHER", "text": "hello channel", "ts": "1778500100.000000"},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{"messages": []map[string]any{}})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	d := newSlackDirective()
	d.Config["followed_channels"] = "#team-x"
	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), d, &CollectOpts{
		Since: since, Until: now, Scope: ScopeAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found *StatusItem
	for i := range items {
		if items[i].Kind == "slack_channel_message" {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected slack_channel_message, got items=%+v", items)
	}
	if found.IsSelf {
		t.Errorf("channel message authored by other should be IsSelf=false, got %+v", found)
	}
	if found.Repository != "#team-x" {
		t.Errorf("expected Repository=#team-x, got %q", found.Repository)
	}
}

func TestSlackCollectDeduplicatesAcrossSources(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	// Same (channel_id, ts) appears as both a mention and a channel
	// history row. Expect a single emitted item, with the more specific
	// kind (slack_mention) preserved.
	const sharedChannel = "C_TEAM"
	const sharedTS = "1778500200.000000"

	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			if req.Query.Get("types") == "public_channel,private_channel" {
				return http.StatusOK, slackOK(map[string]any{
					"channels": []map[string]any{
						{"id": sharedChannel, "name": "team-x", "is_channel": true},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{"channels": []map[string]any{}})
		case "search.messages":
			if strings.Contains(req.Query.Get("query"), "from:<@") {
				return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
			}
			return http.StatusOK, slackOK(map[string]any{
				"messages": map[string]any{
					"matches": []map[string]any{
						{
							"type": "message", "user": "U_OTHER",
							"text": "hey <@" + userID + ">", "ts": sharedTS,
							"channel": map[string]any{"id": sharedChannel, "name": "team-x"},
						},
					},
				},
			})
		case "conversations.history":
			if req.Query.Get("channel") == sharedChannel {
				return http.StatusOK, slackOK(map[string]any{
					"messages": []map[string]any{
						{"type": "message", "user": "U_OTHER", "text": "hey <@" + userID + ">", "ts": sharedTS},
					},
				})
			}
			return http.StatusOK, slackOK(map[string]any{"messages": []map[string]any{}})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	d := newSlackDirective()
	d.Config["followed_channels"] = "#team-x"
	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), d, &CollectOpts{
		Since: since, Until: now, Scope: ScopeAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	var matches []StatusItem
	for _, it := range items {
		if f := it.Fields["ts"]; f == sharedTS {
			matches = append(matches, it)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected one deduplicated item for ts=%s, got %d (%+v)", sharedTS, len(matches), matches)
	}
	if matches[0].Kind != "slack_mention" {
		t.Errorf("expected slack_mention to win dedupe, got %q (%+v)", matches[0].Kind, matches[0])
	}
}

func TestSlackRegistryRegisters(t *testing.T) {
	reg := NewRegistry(time.Now)
	for _, name := range reg.Names() {
		if name == "slack" {
			return
		}
	}
	t.Fatalf("slack collector not registered; names=%v", reg.Names())
}

// TestSlackCollectSkipsDeletedAndArchivedDMs guards the request-reduction
// optimization: DMs with deactivated peers and archived MPIMs should not
// produce any conversations.history calls.
func TestSlackCollectSkipsDeletedAndArchivedDMs(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	srv, state := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{
				"channels": []map[string]any{
					{"id": "D_DEAD", "is_im": true, "user": "U_GONE", "is_user_deleted": true},
					{"id": "G_OLD_MPIM", "is_mpim": true, "name": "mpdm-old", "is_archived": true},
					{"id": "D_LIVE", "is_im": true, "user": "U_PEER"},
				},
			})
		case "conversations.history":
			return http.StatusOK, slackOK(map[string]any{
				"messages": []map[string]any{
					{"type": "message", "user": "U_PEER", "text": "alive", "ts": "1778500000.000000"},
				},
			})
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	})
	if err != nil {
		t.Fatal(err)
	}
	historyChans := map[string]int{}
	for _, r := range state.snapshot() {
		if pathFor(r.Path) != "conversations.history" {
			continue
		}
		historyChans[r.Query.Get("channel")]++
	}
	if historyChans["D_DEAD"] != 0 {
		t.Errorf("expected zero history calls for deactivated-peer DM, got %d", historyChans["D_DEAD"])
	}
	if historyChans["G_OLD_MPIM"] != 0 {
		t.Errorf("expected zero history calls for archived MPIM, got %d", historyChans["G_OLD_MPIM"])
	}
	if historyChans["D_LIVE"] != 1 {
		t.Errorf("expected exactly one history call for live DM, got %d", historyChans["D_LIVE"])
	}
	var dm *StatusItem
	for i := range items {
		if items[i].Kind == "slack_dm" {
			dm = &items[i]
		}
	}
	if dm == nil {
		t.Fatalf("expected slack_dm item from live DM, got items=%+v", items)
	}
}

// TestSlackCallAPIRetriesOn429 guards the rate-limit recovery path:
// a transient 429 is followed by a success and the caller never sees the
// failure.
func TestSlackCallAPIRetriesOn429(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	var (
		mu          sync.Mutex
		historyHits int
	)
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{
				"channels": []map[string]any{
					{"id": "D_DM1", "is_im": true, "user": "U_PEER"},
				},
			})
		case "conversations.history":
			mu.Lock()
			historyHits++
			n := historyHits
			mu.Unlock()
			if n == 1 {
				// First attempt: simulate Slack throttling us. Use a
				// Retry-After of 0 seconds so the test doesn't wait.
				return http.StatusTooManyRequests, map[string]any{"ok": false, "error": "ratelimited"}
			}
			return http.StatusOK, slackOK(map[string]any{
				"messages": []map[string]any{
					{"type": "message", "user": "U_PEER", "text": "after retry", "ts": "1778500000.000000"},
				},
			})
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		case "users.info":
			return http.StatusOK, slackOK(map[string]any{
				"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
			})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	items, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	})
	if err != nil {
		t.Fatalf("expected transparent retry to mask the 429, got error: %v", err)
	}
	mu.Lock()
	gotHits := historyHits
	mu.Unlock()
	if gotHits != 2 {
		t.Errorf("expected 2 conversations.history calls (429 + retry), got %d", gotHits)
	}
	var dm *StatusItem
	for i := range items {
		if items[i].Kind == "slack_dm" {
			dm = &items[i]
		}
	}
	if dm == nil {
		t.Fatalf("expected slack_dm after retry, got items=%+v", items)
	}
}

// TestSlackCallAPIGivesUpAfterMaxRetries guards the upper bound on the
// retry loop: an endpoint that always returns 429 must eventually
// surface a useful error instead of looping forever.
//
// The exponential backoff (1+2+4+8 seconds) means this test sleeps ~15s
// of real time on the failure path, so it skips under `go test -short`.
func TestSlackCallAPIGivesUpAfterMaxRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("skip: backoff schedule sleeps ~15s of real time")
	}
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	var (
		mu          sync.Mutex
		historyHits int
	)
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{
				"channels": []map[string]any{
					{"id": "D_DM1", "is_im": true, "user": "U_PEER"},
				},
			})
		case "conversations.history":
			mu.Lock()
			historyHits++
			mu.Unlock()
			return http.StatusTooManyRequests, map[string]any{"ok": false, "error": "ratelimited"}
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	_, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	})
	if err == nil {
		t.Fatalf("expected a surfaced 429 after retries are exhausted")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "after") {
		t.Errorf("expected error to mention 429 and retry count, got: %v", err)
	}
	mu.Lock()
	gotHits := historyHits
	mu.Unlock()
	// 1 initial + slackMaxRetries retries.
	want := 1 + slackMaxRetries
	if gotHits != want {
		t.Errorf("expected %d total attempts (1 + %d retries), got %d", want, slackMaxRetries, gotHits)
	}
}

// TestSlackCollectFanOutBoundedByConcurrency guards both the parallelism
// (no requests are dropped or duplicated) and the worker-count bound
// (in-flight requests never exceed history_concurrency).
func TestSlackCollectFanOutBoundedByConcurrency(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"
	const dmCount = 12
	const concurrency = 3

	channels := make([]map[string]any, 0, dmCount)
	for i := 0; i < dmCount; i++ {
		channels = append(channels, map[string]any{
			"id":   fmt.Sprintf("D_PEER%02d", i),
			"is_im": true,
			"user": fmt.Sprintf("U_PEER%02d", i),
		})
	}

	var (
		mu      sync.Mutex
		inFlight int
		peakInFlight int
		seen    = map[string]int{}
	)
	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{"channels": channels})
		case "conversations.history":
			mu.Lock()
			inFlight++
			if inFlight > peakInFlight {
				peakInFlight = inFlight
			}
			ch := req.Query.Get("channel")
			seen[ch]++
			mu.Unlock()
			// Hold the handler open briefly so concurrent workers
			// actually overlap; otherwise even single-threaded code
			// would race through fast enough to look "concurrent".
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return http.StatusOK, slackOK(map[string]any{"messages": []map[string]any{}})
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	d := newSlackDirective()
	d.Config["history_concurrency"] = strconv.Itoa(concurrency)
	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	if _, err := c.Collect(context.Background(), d, &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < dmCount; i++ {
		id := fmt.Sprintf("D_PEER%02d", i)
		if seen[id] != 1 {
			t.Errorf("expected exactly one history call for %s, got %d", id, seen[id])
		}
	}
	if peakInFlight > concurrency {
		t.Errorf("peak in-flight history calls = %d, want <= %d (history_concurrency)", peakInFlight, concurrency)
	}
	if peakInFlight < 2 {
		t.Errorf("expected at least 2 concurrent history calls, peak=%d (fan-out not actually parallel?)", peakInFlight)
	}
}

// TestSlackCollectTolerablePerChannelHistoryErrors guards the
// "skip a broken channel, keep the rest" path: a stale DM that
// conversations.list reports but conversations.history can't read
// (e.g. channel_not_found) must not abort the whole run.
func TestSlackCollectTolerablePerChannelHistoryErrors(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	// We sweep every documented tolerable code to make sure adding a
	// new one to the set wires it through end-to-end.
	tolerableCodes := []string{
		"channel_not_found",
		"not_in_channel",
		"is_archived",
		"access_denied",
		"missing_scope",
	}
	for _, code := range tolerableCodes {
		t.Run(code, func(t *testing.T) {
			srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
				switch pathFor(req.Path) {
				case "auth.test":
					return http.StatusOK, authTestPayload(userID)
				case "conversations.list":
					return http.StatusOK, slackOK(map[string]any{
						"channels": []map[string]any{
							{"id": "D_BROKEN", "is_im": true, "user": "U_STALE"},
							{"id": "D_LIVE", "is_im": true, "user": "U_PEER"},
						},
					})
				case "conversations.history":
					if req.Query.Get("channel") == "D_BROKEN" {
						// Slack returns HTTP 200 with ok:false for these.
						return http.StatusOK, map[string]any{"ok": false, "error": code}
					}
					return http.StatusOK, slackOK(map[string]any{
						"messages": []map[string]any{
							{"type": "message", "user": "U_PEER", "text": "alive", "ts": "1778500000.000000"},
						},
					})
				case "search.messages":
					return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
				case "users.info":
					return http.StatusOK, slackOK(map[string]any{
						"user": map[string]any{"id": req.Query.Get("user"), "name": "user-" + req.Query.Get("user")},
					})
				}
				return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
			})

			c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
			items, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
				Since: since, Until: now, Scope: ScopeSelf,
			})
			if err != nil {
				t.Fatalf("expected tolerable %q to be skipped, got error: %v", code, err)
			}
			var dm *StatusItem
			for i := range items {
				if items[i].Kind == "slack_dm" {
					dm = &items[i]
				}
			}
			if dm == nil {
				t.Fatalf("expected slack_dm from live DM, got items=%+v", items)
			}
		})
	}
}

// TestSlackCollectIntolerableSlackErrorStillFails verifies that
// non-tolerable Slack error codes still surface up — we don't want the
// retry path to silently swallow auth failures or missing scopes.
func TestSlackCollectIntolerableSlackErrorStillFails(t *testing.T) {
	t.Setenv("SLAKKR_SLACK_TEST_TOKEN", "xoxp-good")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	const userID = "U_SELF"

	srv, _ := newSlackServer(t, func(req slackTestRequest) (int, any) {
		switch pathFor(req.Path) {
		case "auth.test":
			return http.StatusOK, authTestPayload(userID)
		case "conversations.list":
			return http.StatusOK, slackOK(map[string]any{
				"channels": []map[string]any{
					{"id": "D_DM1", "is_im": true, "user": "U_PEER"},
				},
			})
		case "conversations.history":
			return http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"}
		case "search.messages":
			return http.StatusOK, slackOK(map[string]any{"messages": map[string]any{"matches": []map[string]any{}}})
		}
		return http.StatusNotFound, map[string]any{"ok": false, "error": "unknown:" + req.Path}
	})

	c := SlackCollector{Clock: func() time.Time { return now }, BaseURL: srv.URL}
	_, err := c.Collect(context.Background(), newSlackDirective(), &CollectOpts{
		Since: since, Until: now, Scope: ScopeSelf,
	})
	if err == nil {
		t.Fatalf("expected invalid_auth to fail the run, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("expected error to mention invalid_auth, got: %v", err)
	}
}

func TestSlackAPIErrorIsTolerable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("plain error"), false},
		{&slackAPIError{Endpoint: "conversations.history", Code: "channel_not_found"}, true},
		{&slackAPIError{Endpoint: "conversations.history", Code: "not_in_channel"}, true},
		{&slackAPIError{Endpoint: "conversations.history", Code: "is_archived"}, true},
		{&slackAPIError{Endpoint: "conversations.history", Code: "access_denied"}, true},
		{&slackAPIError{Endpoint: "conversations.history", Code: "missing_scope"}, true},
		{&slackAPIError{Endpoint: "conversations.history", Code: "invalid_auth"}, false},
		{&slackAPIError{Endpoint: "conversations.history", Code: "ratelimited"}, false},
		{fmt.Errorf("wrap: %w", &slackAPIError{Endpoint: "conversations.history", Code: "channel_not_found"}), true},
	}
	for _, tc := range cases {
		if got := isTolerablePerChannelError(tc.err); got != tc.want {
			t.Errorf("isTolerablePerChannelError(%v) = %v want %v", tc.err, got, tc.want)
		}
	}
}

func TestSlackHistoryConcurrencyParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", defaultHistoryConcurrency},
		{"   ", defaultHistoryConcurrency},
		{"not-a-number", defaultHistoryConcurrency},
		{"0", defaultHistoryConcurrency},
		{"-3", defaultHistoryConcurrency},
		{"1", 1},
		{"8", 8},
		{"9999", maxHistoryConcurrency},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			d := newSlackDirective()
			d.Config["history_concurrency"] = tc.raw
			if got := historyConcurrency(d); got != tc.want {
				t.Errorf("historyConcurrency(%q) = %d want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSlackRetryAfterParsing(t *testing.T) {
	cases := []struct {
		header  string
		attempt int
		want    time.Duration
	}{
		{"", 0, time.Second},
		{"", 1, 2 * time.Second},
		{"", 2, 4 * time.Second},
		{"", 30, slackMaxRetryAfter}, // exponential overflow clamped
		{"  ", 0, time.Second},
		{"abc", 0, time.Second},
		{"0", 0, time.Second}, // zero falls back to backoff
		{"3", 0, 3 * time.Second},
		{"60", 0, slackMaxRetryAfter}, // header value clamped
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("header=%q,attempt=%d", tc.header, tc.attempt), func(t *testing.T) {
			if got := slackRetryAfter(tc.header, tc.attempt); got != tc.want {
				t.Errorf("slackRetryAfter(%q, %d) = %s want %s", tc.header, tc.attempt, got, tc.want)
			}
		})
	}
}
