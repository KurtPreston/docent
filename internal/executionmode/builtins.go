package executionmode

// Built-in instruction strings preserve the wording previously hard-coded in
// internal/ai (the more thorough ollama variant; cursor's was a shorter
// paraphrase of the same intent).
const (
	dailyPlanInstruction = "Create a practical daily plan. Section `## Yesterday` summarizes factual work from the aggregated activity below. Section `## Today` proposes a focused plan for today using that activity."

	recentActivityInstruction = "Summarize the developer's recent activity. Activity below is grouped by Git repository where each item's repository field is set (usually org/repo); treat it as ground truth. Return one Markdown document with a brief executive summary at the top and noteworthy callouts. Do not invent activity not present in the input."
)

// BuiltinModes returns the three built-in execution modes in canonical
// menu order. Returned values are fresh on each call, so callers may mutate
// them without affecting future loads.
//
// `daily-plan` and `custom-prompt` pin Scope to ScopeInvolved because they
// have a single intended audience (the user's own day). `recent-activity`
// leaves Scope unset on purpose so Resolve prompts for it interactively,
// letting the user broaden to `all` or narrow to `self` on a per-run basis.
func BuiltinModes() []ExecutionMode {
	return []ExecutionMode{
		{
			ID:       BuiltinDailyPlan,
			Name:     "Daily plan",
			Lookback: &Lookback{Kind: LookbackKindPreviousWeekday},
			Prompt:   &Prompt{Instruction: dailyPlanInstruction},
			Scope:    ScopeInvolved,
		},
		{
			ID:   BuiltinRecentActivity,
			Name: "Recent activity",
			// Lookback and Scope intentionally left nil/unset: Resolve
			// prompts the user for days (default 7) and scope (default
			// involved) at runtime, matching the README's "default 7,
			// or prompt" lookback and the documented behavior that any
			// property a mode omits is asked interactively.
			Prompt: &Prompt{Instruction: recentActivityInstruction},
		},
		{
			ID:       BuiltinCustomPrompt,
			Name:     "Custom prompt",
			Lookback: &Lookback{Kind: LookbackKindDays, Days: 7},
			// Prompt left nil on purpose: the user supplies the prompt
			// interactively (or via --prompt / --prompt-file).
			Scope: ScopeInvolved,
		},
	}
}
