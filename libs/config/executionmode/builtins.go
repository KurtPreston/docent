package executionmode

// Built-in instruction strings preserve the wording previously hard-coded in
// internal/ai (the more thorough ollama variant; cursor's was a shorter
// paraphrase of the same intent).
const (
	dailyPlanInstruction = "Create a practical daily plan. Section `## Yesterday` summarizes factual work from the correlated work items below. Section `## Today` proposes a focused plan for today using that activity."

	recentActivityInstruction = "Summarize the developer's recent activity. Activity below is grouped into correlated work items (ticket/branch with linked PRs); treat it as ground truth. Return one Markdown document with a brief executive summary at the top and noteworthy callouts. Do not invent activity not present in the input."

	prsInstruction = "List the developer's open GitHub pull requests grouped into `Ready for review:` (not a draft and all checks passing) and `Work in progress:` (everything else). For each PR emit a Markdown bullet linking the Jira ticket key (when present in the title) to the PR URL, followed by the PR title with the Jira ticket stripped. This mode is rendered deterministically; the instruction is a fallback description only."
)

// BuiltinModes returns the three built-in execution modes in canonical
// menu order. Returned values are fresh on each call, so callers may mutate
// them without affecting future loads.
//
// `daily-plan` and `custom-prompt` pin Scope to ScopeInvolved because they
// have a single intended audience (the user's own day). `recent-activity`
// leaves Scope unset on purpose so Resolve prompts for it interactively,
// letting the user broaden to `all` or narrow to `self` on a per-run basis.
//
// `recent-activity` and `custom-prompt` leave Lookback nil so Resolve asks
// the user for the lookback size (default 7 days) at runtime; `daily-plan`
// pins the previous-weekday window since it always plans around one day.
func BuiltinModes() []ExecutionMode {
	return []ExecutionMode{
		{
			ID:       BuiltinDailyPlan,
			Name:     "Daily plan",
			Lookback: &Lookback{Kind: LookbackKindPreviousWeekday},
			Prompt:   &Prompt{Instruction: dailyPlanInstruction},
			Scope:    ScopeInvolved,
			Collect:  CollectBoth,
		},
		{
			ID:   BuiltinRecentActivity,
			Name: "Recent activity",
			// Lookback and Scope intentionally left nil/unset: Resolve
			// prompts the user for days (default 7) and scope (default
			// involved) at runtime, matching the README's "default 7,
			// or prompt" lookback and the documented behavior that any
			// property a mode omits is asked interactively.
			// Collect defaults to events (activity timeline).
			Prompt:  &Prompt{Instruction: recentActivityInstruction},
			Collect: CollectEvents,
		},
		{
			ID:   BuiltinPRs,
			Name: "Pull request status",
			// `prs` lists your currently-open PRs, not a time window of
			// activity. Lookback is pinned to previous-weekday only so
			// Resolve never prompts; the GitHub review-readiness
			// collection ignores the window and queries `--state open`.
			Lookback: &Lookback{Kind: LookbackKindPreviousWeekday},
			Prompt:   &Prompt{Instruction: prsInstruction},
			Scope:    ScopeSelf,
			Collect:  CollectState,
			// Only the GitHub collectors can answer "what are my open
			// PRs and are they ready for review"; skip everything else.
			Collectors: []string{"github", "github-enterprise"},
		},
		{
			ID:   BuiltinCustomPrompt,
			Name: "Custom prompt",
			// Custom prompt is always last in the menu. Lookback left
			// nil on purpose: Resolve prompts the user for days
			// (default 7) at runtime, matching recent-activity's
			// "default 7, or prompt" lookback. --days still overrides.
			// Prompt left nil on purpose: the user supplies the prompt
			// interactively (or via --prompt / --prompt-file).
			Scope:   ScopeInvolved,
			Collect: CollectEvents,
		},
	}
}
