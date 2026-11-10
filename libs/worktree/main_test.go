package worktree

import (
	"os"
	"testing"

	"github.com/KurtPreston/docent/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m))
}
