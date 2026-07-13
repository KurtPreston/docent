package automation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidationError lists human-readable problems with automation config.
type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Problems, "; ")
}

// ValidateRules checks structural invariants of a rule list.
func ValidateRules(rules []Rule) error {
	var problems []string
	seen := map[string]bool{}
	for i, r := range rules {
		path := fmt.Sprintf("automations[%d]", i)
		id := strings.TrimSpace(r.ID)
		if id == "" {
			problems = append(problems, path+".id is required")
		} else if !idPattern.MatchString(id) {
			problems = append(problems, fmt.Sprintf("%s.id %q must match %s", path, id, idPattern.String()))
		} else if seen[id] {
			problems = append(problems, fmt.Sprintf("%s.id is duplicated (%q)", path, id))
		} else {
			seen[id] = true
		}
		problems = append(problems, validateTrigger(path+".trigger", r.Trigger)...)
		if r.Conditions.Cooldown != "" {
			if _, err := ParseDuration(r.Conditions.Cooldown); err != nil {
				problems = append(problems, fmt.Sprintf("%s.conditions.cooldown %q is not a valid duration", path, r.Conditions.Cooldown))
			}
		}
		if len(r.Actions) == 0 {
			problems = append(problems, path+".actions must contain at least one action")
		}
		for j, a := range r.Actions {
			problems = append(problems, validateAction(fmt.Sprintf("%s.actions[%d]", path, j), a)...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return ValidationError{Problems: problems}
}

// ParseDuration accepts time.ParseDuration forms plus a trailing "d" for days.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "d")))
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func validateTrigger(path string, t Trigger) []string {
	var problems []string
	typ := strings.TrimSpace(t.Type)
	switch typ {
	case "", "signal":
		if t.Match.Text != "" {
			if _, err := regexp.Compile(t.Match.Text); err != nil {
				problems = append(problems, fmt.Sprintf("%s.match.text is not a valid regex: %v", path, err))
			}
		}
	case "transition":
		if strings.TrimSpace(t.When.Field) == "" {
			problems = append(problems, path+".when.field is required for transition triggers")
		}
	case "schedule":
		if strings.TrimSpace(t.Cron) == "" && strings.TrimSpace(t.At) == "" {
			problems = append(problems, path+" requires cron or at for schedule triggers")
		}
		if at := strings.TrimSpace(t.At); at != "" {
			if !timeOfDayOK(at) {
				problems = append(problems, fmt.Sprintf("%s.at %q must be HH:MM", path, at))
			}
		}
	default:
		problems = append(problems, fmt.Sprintf("%s.type %q is unknown (expected signal, transition, or schedule)", path, typ))
	}
	return problems
}

func validateAction(path string, a Action) []string {
	var problems []string
	typ := strings.TrimSpace(a.Type)
	switch typ {
	case "webhook":
		if strings.TrimSpace(a.URL) == "" {
			problems = append(problems, path+".url is required for webhook actions")
		}
	case "shell":
		if strings.TrimSpace(a.Command) == "" {
			problems = append(problems, path+".command is required for shell actions")
		}
	case "jira-comment":
		if strings.TrimSpace(a.Body) == "" {
			problems = append(problems, path+".body is required for jira-comment actions")
		}
	case "slack-post":
		if strings.TrimSpace(a.Body) == "" {
			problems = append(problems, path+".body is required for slack-post actions")
		}
	case "agent", "agent-inline":
		if strings.TrimSpace(a.Prompt) == "" {
			problems = append(problems, path+".prompt is required for agent actions")
		}
		wd := strings.TrimSpace(a.Workdir)
		if wd != "" && wd != "worktree" && wd != "open_path" {
			problems = append(problems, fmt.Sprintf("%s.workdir %q must be worktree or open_path", path, wd))
		}
	case "report":
		if strings.TrimSpace(a.Mode) == "" {
			problems = append(problems, path+".mode is required for report actions")
		}
	case "open":
		// no required fields
	case "":
		problems = append(problems, path+".type is required")
	default:
		problems = append(problems, fmt.Sprintf("%s.type %q is unknown", path, typ))
	}
	return problems
}

func timeOfDayOK(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return false
	}
	h, m := parts[0], parts[1]
	if len(h) < 1 || len(h) > 2 || len(m) != 2 {
		return false
	}
	hi, err1 := atoi(h)
	mi, err2 := atoi(m)
	return err1 == nil && err2 == nil && hi >= 0 && hi <= 23 && mi >= 0 && mi <= 59
}

func atoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
