package collectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
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

func (c GoogleCalendarCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	rawURL := strings.TrimSpace(directive.Config["ical_url"])
	if rawURL == "" {
		rawURL = strings.TrimSpace(directive.Config["url"])
	}
	tokenKey := directive.CredentialRefs["ical_url"]
	if tokenKey == "" {
		tokenKey = directive.CredentialRefs["url"]
	}
	if tokenKey != "" {
		userdataDir := ""
		if opts != nil {
			userdataDir = opts.UserdataDir
		}
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
	res, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, calendarIcalHTTPError(res)
	}
	events := parseICSEvents(string(body), c.Clock(), 14)
	now := c.Clock()
	if len(events) == 0 {
		return []StatusItem{{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "google-calendar",
			Kind:        "calendar",
			Title:       directive.Name,
			Summary:     "No upcoming events parsed from iCal in the next two weeks.",
			Severity:    "info",
			ObservedAt:  now,
		}}, nil
	}
	items := make([]StatusItem, 0, len(events))
	for _, ev := range events {
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "google-calendar",
			Kind:        "calendar_event",
			Title:       ev.summary,
			Summary:     ev.start.Format(time.RFC3339),
			Severity:    "info",
			ObservedAt:  now,
			Fields: map[string]string{
				"start": ev.start.UTC().Format(time.RFC3339),
			},
		})
	}
	return items, nil
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
	summary string
}

func unfoldICS(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n ", "")
}

func parseICSEvents(raw string, now time.Time, horizonDays int) []icsEvent {
	text := unfoldICS(raw)
	blocks := strings.Split(text, "BEGIN:VEVENT")
	if len(blocks) < 2 {
		return nil
	}
	until := now.Add(time.Duration(horizonDays) * 24 * time.Hour)
	var out []icsEvent
	for _, b := range blocks[1:] {
		end := strings.Index(b, "END:VEVENT")
		if end >= 0 {
			b = b[:end]
		}
		var startStr, summary string
		for _, line := range strings.Split(b, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToUpper(line), "DTSTART") {
				if idx := strings.LastIndex(line, ":"); idx >= 0 {
					startStr = strings.TrimSpace(line[idx+1:])
				}
			}
			if strings.HasPrefix(strings.ToUpper(line), "SUMMARY") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					summary = strings.TrimSpace(line[idx+1:])
				}
			}
		}
		if startStr == "" || summary == "" {
			continue
		}
		t, ok := parseICSDate(startStr)
		if !ok || t.Before(now) || t.After(until) {
			continue
		}
		out = append(out, icsEvent{start: t, summary: summary})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	if len(out) > 15 {
		out = out[:15]
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
