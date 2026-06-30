package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/correlation"
	"github.com/kurt/slakkr-ai/libs/model"
)

// Dashboard matches the legacy docent /sessions payload for the web UI.
type Dashboard struct {
	GeneratedAt  string         `json:"generatedAt"`
	Backend      string         `json:"backend"`
	SessionCount int            `json:"sessionCount"`
	GroupCount   int            `json:"groupCount"`
	Groups       []DashboardGroup `json:"groups"`
}

type DashboardGroup struct {
	Key           string            `json:"key"`
	Ticket        string            `json:"ticket,omitempty"`
	Summary       string            `json:"summary,omitempty"`
	JiraStatus    string            `json:"jiraStatus,omitempty"`
	JiraURL       string            `json:"jiraUrl,omitempty"`
	Color         string            `json:"color,omitempty"`
	FG            string            `json:"fg,omitempty"`
	NeedsFollowup bool              `json:"needsFollowup"`
	Sessions      []DashboardSession `json:"sessions"`
	PRs           []DashboardPR      `json:"prs"`
}

type DashboardSession struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Host          string `json:"host,omitempty"`
	Path          string `json:"path,omitempty"`
	Ticket        string `json:"ticket,omitempty"`
	Color         string `json:"color,omitempty"`
	FG            string `json:"fg,omitempty"`
	Live          bool   `json:"live"`
	Status        string `json:"status"`
	NeedsFollowup bool   `json:"needsFollowup"`
	LastActivity  string `json:"lastActivity,omitempty"`
}

type DashboardPR struct {
	PRNumber int    `json:"prNumber"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
	Repo     string `json:"repo,omitempty"`
	State    string `json:"state,omitempty"`
	Draft    bool   `json:"draft"`
	Ticket   string `json:"ticket,omitempty"`
}

// Engine collects sources and builds the dashboard model.
type Engine struct {
	cfg      config.DaemonConfig
	registry *registry.Store
	reg      *collectors.Registry
	mu       sync.RWMutex
	cached   Dashboard
	cachedAt time.Time
}

func New(cfg config.DaemonConfig, reg *registry.Store) *Engine {
	return &Engine{
		cfg:      cfg,
		registry: reg,
		reg:      collectors.NewRegistry(time.Now),
	}
}

func (e *Engine) Refresh(ctx context.Context) (Dashboard, error) {
	if e.cachedAt.IsZero() || time.Since(e.cachedAt) > time.Duration(e.cfg.RefreshSec)*time.Second {
		d, err := e.collect(ctx)
		if err != nil {
			return Dashboard{}, err
		}
		e.mu.Lock()
		e.cached = d
		e.cachedAt = time.Now()
		e.mu.Unlock()
		return d, nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cached, nil
}

func (e *Engine) Snapshot() Dashboard {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cached
}

func (e *Engine) collect(ctx context.Context) (Dashboard, error) {
	since := time.Now().Add(-7 * 24 * time.Hour)
	opts := &collectors.CollectOpts{
		UserdataDir: e.cfg.ConfigDir,
		Since:       since,
		Until:       time.Now(),
		Scope:       collectors.ScopeInvolved,
	}
	signals, err := e.reg.Collect(ctx, e.cfg.Directives, opts)
	if err != nil {
		return Dashboard{}, err
	}
	corrCfg := correlation.Config{TicketPattern: e.cfg.TicketPattern}
	entities := correlation.SignalsToEntities(signals, corrCfg)

	// Enrich session entities from registry + live flags.
	liveNames := map[string]bool{}
	for i := range entities {
		ent := &entities[i]
		if ent.Kind != "session" {
			continue
		}
		name := ent.Title
		if rec, ok := e.registry.Get(name); ok {
			if rec.Color != "" {
				ent.State["color"] = rec.Color
			}
			if rec.Host != "" {
				ent.Coordinates["host"] = rec.Host
			}
			status := registry.SessionStatus(rec)
			ent.State["attention"] = status
			ent.State["lastActivity"] = registry.LatestActivity(rec)
		}
		if ent.State["live"] == "true" {
			liveNames[name] = true
		}
		if ent.State["color"] == "" {
			c := model.ColorForName(name)
			ent.State["color"] = c
			ent.State["fg"] = model.ForegroundForHex(c)
		}
	}

	// Include registry-only sessions that still need follow-up (window closed).
	for name, rec := range e.registry.All() {
		status := registry.SessionStatus(rec)
		if status != "needs-followup" {
			continue
		}
		found := false
		for _, ent := range entities {
			if ent.Kind == "session" && ent.Title == name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		ent := model.Entity{
			ID:    "session:" + name,
			Kind:  "session",
			Title: name,
			State: map[string]string{"attention": status, "live": "false", "lastActivity": registry.LatestActivity(rec)},
			Coordinates: map[string]string{},
		}
		if rec.Host != "" {
			ent.Coordinates["host"] = rec.Host
		}
		if rec.Color != "" {
			ent.State["color"] = rec.Color
			ent.State["fg"] = rec.FG
		}
		entities = append(entities, ent)
	}

	workItems := correlation.BuildWorkItems(entities, corrCfg)
	groups := make([]DashboardGroup, 0, len(workItems))
	liveCount := 0
	for _, wi := range workItems {
		g := DashboardGroup{
			Key:      wi.Key,
			Ticket:   wi.Key,
			Summary:  wi.Title,
			Color:    wi.Color,
			FG:       wi.FG,
			Sessions: []DashboardSession{},
			PRs:      []DashboardPR{},
		}
		if wi.Attention == "needs-followup" || wi.Attention == "working" {
			g.NeedsFollowup = wi.Attention == "needs-followup"
		}
		if g.Color == "" {
			g.Color = model.ColorForName(wi.Key)
			g.FG = model.ForegroundForHex(g.Color)
		}
		for _, ent := range wi.Entities {
			switch ent.Kind {
			case "session":
				live := liveNames[ent.Title] || ent.State["live"] == "true"
				if live {
					liveCount++
				}
				status := ent.State["attention"]
				if status == "" {
					status = "idle"
				}
				ds := DashboardSession{
					Kind:          "session",
					Name:          ent.Title,
					Host:          ent.Coordinates["host"],
					Path:          ent.Coordinates["path"],
					Ticket:        correlation.ParseTicketKey(ent.Title, corrCfg),
					Color:         ent.State["color"],
					FG:            ent.State["fg"],
					Live:          live,
					Status:        status,
					NeedsFollowup: status == "needs-followup",
					LastActivity:  ent.State["lastActivity"],
				}
				if ds.Color == "" {
					ds.Color = model.ColorForName(ds.Name)
					ds.FG = model.ForegroundForHex(ds.Color)
				}
				g.Sessions = append(g.Sessions, ds)
				if ds.NeedsFollowup {
					g.NeedsFollowup = true
				}
			case "ticket", "issue_activity":
				g.Summary = ent.Title
				g.JiraURL = ent.URL
				if ent.State != nil {
					g.JiraStatus = ent.State["status"]
				}
			default:
				if strings.Contains(ent.Kind, "pr") {
					num := 0
					if ent.Coordinates != nil {
						_ = ent.Coordinates["number"]
					}
					g.PRs = append(g.PRs, DashboardPR{
						PRNumber: num,
						Title:    ent.Title,
						URL:      ent.URL,
						Repo:     ent.Coordinates["repo"],
						State:    ent.State["state"],
					})
				}
			}
		}
		groups = append(groups, g)
	}
	return Dashboard{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Backend:      "go",
		SessionCount: liveCount,
		GroupCount:   len(groups),
		Groups:       groups,
	}, nil
}

// Ensure default directives include webhook collector.
func EnsureDirectives(d []userdata.Directive) []userdata.Directive {
	hasWebhook := false
	hasWM := false
	for _, dir := range d {
		if dir.Collector == "webhook" {
			hasWebhook = true
		}
		if dir.Collector == "docent-wm" {
			hasWM = true
		}
	}
	out := append([]userdata.Directive{}, d...)
	if !hasWebhook {
		out = append(out, userdata.Directive{
			ID: "webhook", Name: "Webhook inbox", Collector: "webhook", Enabled: true,
		})
	}
	if !hasWM {
		out = append(out, userdata.Directive{
			ID: "local-wm", Name: "Local docent-wm", Collector: "docent-wm", Enabled: true,
			Config: map[string]string{"base_url": "http://127.0.0.1:39788", "machine": "local"},
		})
	}
	return out
}
