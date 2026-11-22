package eval

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/gateway"
)

// TestLive works every task of both sets out against the real site, with no model: the check to run before an
// evaluation, so a task the data broke is fixed before it costs a model a point. It needs the
// network, so it runs only with ANCHOR_LAB_LIVE=1 (anchor-lab eval -check does the same on the box).
func TestLive(t *testing.T) {
	if os.Getenv("ANCHOR_LAB_LIVE") != "1" {
		t.Skip("set ANCHOR_LAB_LIVE=1 to check the tasks against the real site")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, tools, err := gateway.Open(ctx, MCPURL(), "anchor-lab-test", gateway.DefaultLimits)
	if err != nil {
		t.Fatalf("the MCP server: %v", err)
	}
	src := NewSources("anchor-lab-test")
	for _, l := range append(Check(ctx, src, tools), CheckTasks(ctx, HardTasks, src, tools)...) {
		if l.Err != nil {
			t.Errorf("%s (level %d): %v", l.ID, l.Level, l.Err)
			continue
		}
		t.Logf("%-36s %d  %s", l.ID, l.Level, l.Answer)
	}
}
