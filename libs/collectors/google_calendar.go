package collectors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/libs/config/userdata"
)

type GoogleCalendarCollector struct {
	Clock func() time.Time
	HTTP  *http.Client
}

func (c GoogleCalendarCollector) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Collect returns calendar events whose DTSTART falls in [opts.Since, window end] (read-only GET of iCal).
//
// Scope semantics: the iCal feed is the user's personal calendar by
// definition (the URL is a per-user secret), so events on it are events
// the user has either organized or accepted. All three scopes therefore
// emit the same set today, and every item is marked IsSelf=true.
//
// A future improvement could split ScopeSelf out by inspecting the
// ORGANIZER property and comparing against a new config.user_email
// (events where the user is only an attendee would drop to IsSelf=false).
// That is intentionally out of scope for the current iCal-only collector.
func (c GoogleCalendarCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	rawURL := strings.TrimSpace(directive.Config["ical_url"])
	if rawURL == "" {
		rawURL = strings.TrimSpace(directive.Config["url"])
	}
	tokenKey := directive.CredentialRefs["ical_url"]
	if tokenKey == "" {
		tokenKey = directive.CredentialRefs["url"]
	}
	userdataDir := ""
	since := time.Time{}
	if opts != nil {
		userdataDir = opts.UserdataDir
		since = opts.Since
	}
	if tokenKey != "" {
		if v := userdata.ResolveEnv(userdataDir, tokenKey); v != "" {
			rawURL = v
		}
	}
	if rawURL == "" {
		return nil, fmt.Errorf("config.ical_url is required (secret iCal link from Google Calendar settings)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	res, body, err := doAndReadHTTP(c.client(), req, 8<<20, opts, directive.ID)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, calendarIcalHTTPError(res)
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	events := parseICSEventsInWindow(string(body), since, now)
	var items []StatusItem
	for _, ev := range events {
		fields := map[string]string{
			"start": ev.start.UTC().Format(time.RFC3339),
		}
		if !ev.end.IsZero() {
			fields["end"] = ev.end.UTC().Format(time.RFC3339)
			if ev.end.After(ev.start) {
				fields["duration"] = ev.end.Sub(ev.start).String()
			}
		}
		if ev.uid != "" {
			fields["uid"] = ev.uid
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Source:      "google-calendar",
			Kind:        "calendar_event",
			Title:       ev.summary,
			Summary:     ev.start.Format(time.RFC3339),
			Severity:    "info",
			ObservedAt:  ev.start.UTC(),
			IsSelf:      true,
			Fields:      fields,
		})
	}
	return items, nil
}

// ValidateDirective checks that an iCal URL is configured (either inline or
// via credential_refs) and that the URL responds with a 2xx GET.
func (c GoogleCalendarCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	rawURL := strings.TrimSpace(directive.Config["ical_url"])
	if rawURL == "" {
		rawURL = strings.TrimSpace(directive.Config["url"])
	}
	tokenKey := strings.TrimSpace(directive.CredentialRefs["ical_url"])
	if tokenKey == "" {
		tokenKey = strings.TrimSpace(directive.CredentialRefs["url"])
	}
	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}
	if tokenKey != "" {
		if v := userdata.ResolveEnv(userdataDir, tokenKey); v != "" {
			rawURL = v
		} else if rawURL == "" {
			return []ValidationIssue{{
				Field:       "credential_refs.ical_url",
				Message:     fmt.Sprintf("iCal URL env %q is empty", tokenKey),
				Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", tokenKey, userdataDir),
			}}
		}
	}
	if rawURL == "" {
		return []ValidationIssue{{
			Field:       "config.ical_url",
			Message:     "no iCal URL configured",
			Remediation: "set config.ical_url or credential_refs.ical_url to the secret iCal address from Google Calendar settings",
		}}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []ValidationIssue{{
			Field:       "ical_url",
			Message:     "iCal URL is not a valid URL",
			Remediation: "copy the secret iCal address from Google Calendar settings (Integrate calendar)",
		}}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return []ValidationIssue{{
			Field:       "ical_url",
			Message:     fmt.Sprintf("iCal probe request build failed: %v", err),
			Remediation: "verify the iCal URL",
		}}
	}
	res, _, err := doAndReadHTTP(c.client(), req, 1<<20, nil, directive.ID)
	if err != nil {
		return []ValidationIssue{{
			Field:       "ical_url",
			Message:     fmt.Sprintf("iCal probe failed: %v", err),
			Remediation: "verify network connectivity and the iCal URL",
		}}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return []ValidationIssue{{
			Field:       "ical_url",
			Message:     calendarIcalHTTPError(res).Error(),
			Remediation: "re-copy the secret iCal address from Google Calendar settings (Integrate calendar)",
		}}
	}
	return nil
}

func calendarIcalHTTPError(res *http.Response) error {
	if res == nil {
		return fmt.Errorf("iCal feed: empty response")
	}
	switch res.StatusCode {
	case http.StatusForbidden:
		return fmt.Errorf("iCal feed returned 403 Forbidden. Google is rejecting this URL: the secret link may be invalid, reset, or revoked. Re-copy \"Secret address in iCal format\" from the calendar's settings (Integrate calendar) into userdata/.env (or the matching SLAKKR_*_ICAL_URL in your environment)")
	case http.StatusNotFound:
		return fmt.Errorf("iCal feed returned 404 Not Found: the URL may be wrong or the calendar export was removed")
	case http.StatusUnauthorized:
		return fmt.Errorf("iCal feed returned 401 Unauthorized: the feed URL or token in the link may be invalid or expired")
	default:
		if res.StatusCode >= 500 {
			return fmt.Errorf("iCal feed server error: %s (try again later)", res.Status)
		}
		return fmt.Errorf("iCal feed request failed: %s", res.Status)
	}
}

type icsEvent struct {
	start   time.Time
	end     time.Time
	summary string
	uid     string
}

func unfoldICS(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n ", "")
}

func parseICSEventsInWindow(raw string, since, now time.Time) []icsEvent {
	text := unfoldICS(raw)
	blocks := strings.Split(text, "BEGIN:VEVENT")
	if len(blocks) < 2 {
		return nil
	}
	var out []icsEvent
	for _, b := range blocks[1:] {
		endBlock := strings.Index(b, "END:VEVENT")
		if endBlock >= 0 {
			b = b[:endBlock]
		}
		var startStr, endStr, summary, uid string
		for _, line := range strings.Split(b, "\n") {
			line = strings.TrimSpace(line)
			upper := strings.ToUpper(line)
			if strings.HasPrefix(upper, "DTSTART") {
				if idx := strings.LastIndex(line, ":"); idx >= 0 {
					startStr = strings.TrimSpace(line[idx+1:])
				}
			}
			if strings.HasPrefix(upper, "DTEND") {
				if idx := strings.LastIndex(line, ":"); idx >= 0 {
					endStr = strings.TrimSpace(line[idx+1:])
				}
			}
			if strings.HasPrefix(upper, "SUMMARY") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					summary = strings.TrimSpace(line[idx+1:])
				}
			}
			if strings.HasPrefix(upper, "UID") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					uid = strings.TrimSpace(line[idx+1:])
				}
			}
		}
		if startStr == "" || summary == "" {
			continue
		}
		t, ok := parseICSDate(startStr)
		if !ok || t.Before(since) || t.After(now) {
			continue
		}
		ev := icsEvent{start: t, summary: summary, uid: uid}
		if endStr != "" {
			if te, ok := parseICSDate(endStr); ok {
				ev.end = te
			}
		}
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func parseICSDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 8 && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		t, err := time.ParseInLocation("20060102", s, time.Local)
		return t, err == nil
	}
	if strings.HasSuffix(s, "Z") {
		t, err := time.Parse("20060102T150405Z", s)
		return t, err == nil
	}
	if len(s) >= 15 && strings.Contains(s, "T") {
		t, err := time.ParseInLocation("20060102T150405", s[:15], time.Local)
		return t, err == nil
	}
	return time.Time{}, false
}
