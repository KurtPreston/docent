package workflow

import (
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

// DailyPlanFromOutput maps AI output into a persisted daily plan file.
func DailyPlanFromOutput(date time.Time, out ai.PlanningOutput, now time.Time) userdata.DailyPlanFile {
	plan := userdata.DailyPlanFile{
		Date:        date.Format("2006-01-02"),
		Summary:     out.Summary,
		FollowUps:   append([]string(nil), out.FollowUps...),
		Deferrals:   append([]string(nil), out.Deferrals...),
		NonGoals:    append([]string(nil), out.NonGoals...),
		GeneratedAt: userdata.YAMLDateTime{Time: now.UTC()},
	}
	if out.PrimaryFocus != nil {
		plan.PrimaryFocus = userdata.PlanFocus{
			TaskID: out.PrimaryFocus.TaskID,
			Title:  out.PrimaryFocus.Title,
			Reason: out.PrimaryFocus.Reason,
		}
	}
	for _, b := range out.SecondaryFocus {
		plan.SecondaryFocus = append(plan.SecondaryFocus, userdata.PlanFocus{
			TaskID: b.TaskID,
			Title:  b.Title,
			Reason: b.Reason,
		})
	}
	if plan.PrimaryFocus.Title == "" && len(out.FocusBlocks) > 0 {
		b := out.FocusBlocks[0]
		plan.PrimaryFocus = userdata.PlanFocus{TaskID: b.TaskID, Title: b.Title, Reason: b.Reason}
	}
	return plan
}
