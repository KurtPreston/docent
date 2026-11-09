package collectors

import (
	"regexp"
	"strings"

	"github.com/KurtPreston/docent/libs/worktree"
)

// maxElectedSlug caps the title part of an elected branch name, so a long task
// title still gives a name that reads well in a branch list.
const maxElectedSlug = 48

// slugRuns matches each run of characters that cannot appear in a slug.
var slugRuns = regexp.MustCompile(`[^a-z0-9]+`)

// electBranch names the branch for a task whose brief doesn't: the prefix and
// a slug of the title, or of the task ID when the title has nothing to slug.
// The name depends only on the task, so every poll and every retry lands on
// the same branch and continues earlier work rather than starting over.
func electBranch(prefix, title, id string) string {
	for _, source := range []string{title, id} {
		slug := slugify(source)
		if slug == "" {
			continue
		}
		if name := prefix + slug; worktree.CheckBranchName(name) == nil {
			return name
		}
	}
	return ""
}

// slugify lower-cases s and joins its words with dashes, cutting a long result
// at a word boundary.
func slugify(s string) string {
	slug := strings.Trim(slugRuns.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(slug) <= maxElectedSlug {
		return slug
	}

	// Cut at the last dash that fits, or mid-word if the first word is too long
	slug = slug[:maxElectedSlug]
	if cut := strings.LastIndex(slug, "-"); cut > 0 {
		slug = slug[:cut]
	}
	return strings.Trim(slug, "-")
}
