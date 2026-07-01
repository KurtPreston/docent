package collectors

import (
	"context"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

func TestWSMCollector_ValidateDirective(t *testing.T) {
	c := WSMCollector{Clock: time.Now}
	issues := c.ValidateDirective(context.Background(), userdata.Directive{ID: "wm"}, &ValidateOpts{})
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
}
