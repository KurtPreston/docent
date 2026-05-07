package collectors

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

// Collect emits commits and reflog rows since opts.Since across resolved repo directories.
func (c LocalGitCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	dirs, err := localGitRepoDirs(directive, opts, expand)
	if err != nil {
		return nil, err
	}
	sinceISO := since.UTC().Format(time.RFC3339)
	var out []StatusItem
	commitTimes := map[string]time.Time{}
	for _, abs := range dirs {
		baseLabel := strings.TrimSpace(directive.ProjectID)
		if baseLabel == "" {
			baseLabel = filepath.Base(abs)
		}
		logOut, err := gitOutput(ctx, abs, "log", "--all", "--no-merges", "--since="+sinceISO, "--pretty=format:%H%x09%aI%x09%an%x09%s")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(logOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 4 {
				continue
			}
			hash := strings.TrimSpace(parts[0])
			iso := strings.TrimSpace(parts[1])
			author := strings.TrimSpace(parts[2])
			subject := strings.TrimSpace(parts[3])
			obs, err := time.Parse(time.RFC3339, iso)
			if err != nil {
				if obs, err = time.Parse("2006-01-02 15:04:05 -0700", strings.ReplaceAll(iso, "T", " ")); err != nil {
					continue
				}
			}
			if obs.Before(since) || obs.After(now) {
				continue
			}
			short := hash
			if len(hash) > 7 {
				short = hash[:7]
			}
			title := subject
			if len(dirs) > 1 {
				title = fmt.Sprintf("(%s) %s", filepath.Base(abs), subject)
			}
			out = append(out, StatusItem{
				DirectiveID: directive.ID,
				ProjectID:   baseLabel,
				Source:      "local-git",
				Kind:        "commit",
				Title:       title,
				Summary:     fmt.Sprintf("%s %s — %s", short, author, iso),
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"path":       abs,
					"hash":       hash,
					"short_hash": short,
					"author":     author,
					"iso":        iso,
					"subject":    subject,
				},
			})
		}
		refOut, err := gitOutput(ctx, abs, "reflog", "--since="+sinceISO, "--date=iso", "--pretty=format:%H%x09%gd%x09%gs")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(refOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 3 {
				continue
			}
			hash := strings.TrimSpace(parts[0])
			gd := strings.TrimSpace(parts[1])
			gs := strings.TrimSpace(parts[2])
			obs, ok := commitTimes[hash]
			if !ok {
				ci, err := gitOutput(ctx, abs, "show", "-s", "--format=%cI", hash)
				if err != nil {
					continue
				}
				ci = strings.TrimSpace(ci)
				var perr error
				obs, perr = time.Parse(time.RFC3339, ci)
				if perr != nil {
					continue
				}
				commitTimes[hash] = obs
			}
			if obs.Before(since) || obs.After(now) {
				continue
			}
			short := hash
			if len(hash) > 7 {
				short = hash[:7]
			}
			title := gd + " " + gs
			if len(dirs) > 1 {
				title = fmt.Sprintf("(%s) %s %s", filepath.Base(abs), gd, gs)
			}
			out = append(out, StatusItem{
				DirectiveID: directive.ID,
				ProjectID:   baseLabel,
				Source:      "local-git",
				Kind:        "reflog",
				Title:       title,
				Summary:     short,
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"path":       abs,
					"hash":       hash,
					"short_hash": short,
					"gd":         gd,
					"gs":         gs,
				},
			})
		}
	}
	return out, nil
}

func localGitRepoDirs(directive userdata.Directive, opts *CollectOpts, expand func(string) string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	for _, p := range directive.Paths {
		p = expand(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
				continue
			}
			if !seen[p] {
				seen[p] = true
				dirs = append(dirs, p)
			}
		}
	}
	if len(dirs) > 0 {
		return dirs, nil
	}
	codeHome := ""
	if opts != nil {
		codeHome = strings.TrimSpace(opts.CodeHome)
	}
	if codeHome == "" {
		return nil, fmt.Errorf("local-git: set code_home in config or paths on the directive")
	}
	codeHome = expand(codeHome)
	entries, err := os.ReadDir(codeHome)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cand := filepath.Join(codeHome, e.Name())
		if _, err := os.Stat(filepath.Join(cand, ".git")); err != nil {
			continue
		}
		abs := expand(cand)
		if !seen[abs] {
			seen[abs] = true
			dirs = append(dirs, abs)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("local-git: no git repositories under %s", codeHome)
	}
	return dirs, nil
}

func defaultExpandRepoPath(opts *CollectOpts) func(string) string {
	if opts != nil && opts.ExpandRepoPath != nil {
		return opts.ExpandRepoPath
	}
	return func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		abs, err := filepath.Abs(s)
		if err != nil {
			return filepath.Clean(s)
		}
		return abs
	}
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
