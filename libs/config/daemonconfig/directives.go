package daemonconfig

import "github.com/KurtPreston/docent/libs/config/userdata"

// EnsureDirectives injects the always-on webhook inbox. Live-window polling is
// no longer coupled to the open_trigger provider: to list live windows, declare
// an explicit "cursor" or "wsm" collector directive in config.yaml. Session
// activity also arrives via the ingest API (POST /api/sessions/events).
func EnsureDirectives(d []userdata.Directive) []userdata.Directive {
	hasWebhook := false
	for _, dir := range d {
		if dir.Collector == "webhook" {
			hasWebhook = true
		}
	}
	out := append([]userdata.Directive{}, d...)
	if !hasWebhook {
		out = append(out, userdata.Directive{
			ID: "webhook", Name: "Webhook inbox", Collector: "webhook", Enabled: true,
			// The inbox is drained on read, so collect on every request.
			Events: &userdata.ModeConfig{Poll: userdata.PollConfig{OnRequest: true}},
		})
	}
	return out
}
