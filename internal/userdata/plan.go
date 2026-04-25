package userdata

import (
	"time"
)

// DailyPlanFile is the structured source of truth for a day's attention plan.
type DailyPlanFile struct {
	Date           string       `yaml:"date"`
	PrimaryFocus   PlanFocus    `yaml:"primary_focus"`
	SecondaryFocus []PlanFocus  `yaml:"secondary_focus,omitempty"`
	FollowUps      []string     `yaml:"follow_ups,omitempty"`
	Deferrals      []string     `yaml:"deferrals,omitempty"`
	NonGoals       []string     `yaml:"non_goals,omitempty"`
	Summary        string       `yaml:"summary,omitempty"`
	GeneratedAt    YAMLDateTime `yaml:"generated_at,omitempty"`
}

type PlanFocus struct {
	TaskID string `yaml:"task_id,omitempty"`
	Title  string `yaml:"title"`
	Reason string `yaml:"reason,omitempty"`
}

func (f DailyPlanFile) Validate() error {
	var problems []string
	if f.Date == "" {
		problems = append(problems, "date is required")
	} else if _, err := time.Parse("2006-01-02", f.Date); err != nil {
		problems = append(problems, "date must be YYYY-MM-DD")
	}
	if f.PrimaryFocus.Title == "" {
		problems = append(problems, "primary_focus.title is required")
	}
	return validationResult(problems)
}
