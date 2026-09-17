package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/cohort"
	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/impact"
	"github.com/arnocho/spanline/internal/result"
)

// fixtureActions builds the same drill down the binary offers, on the recorded estate.
func fixtureActions(t *testing.T) *Actions {
	t.Helper()
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	return &Actions{
		Why: func(kubeContext, namespace, workload string) (*result.WhyReport, error) {
			snap, err := src.Snapshot(context.Background(), kubeContext, namespace)
			if err != nil {
				return nil, err
			}
			return cohort.Analyze(snap, cohort.Options{Workload: workload, Namespace: namespace, Now: src.Now(), Window: 24 * time.Hour})
		},
		Impact: func(kubeContext, selector string) (*result.ImpactReport, error) {
			snap, err := src.Snapshot(context.Background(), kubeContext, "")
			if err != nil {
				return nil, err
			}
			return impact.Nodes(snap, selector, impact.Options{Now: src.Now(), TTL: 2 * time.Hour})
		},
	}
}

// TestDrillDownFromTheOverview presses w on a workload and i on a pool, the way the recording
// does, and checks that each opens the matching view with a report rather than an error toast.
func TestDrillDownFromTheOverview(t *testing.T) {
	d := loadData(t)
	d.Why, d.Impact = nil, nil
	d.Actions = fixtureActions(t)

	frames, err := Cast(d, nil, 100, 30, []Key{
		{"", 300 * time.Millisecond},
		{"w", 1200 * time.Millisecond},
		{"1", 400 * time.Millisecond},
		{"down", 200 * time.Millisecond},
		{"down", 200 * time.Millisecond},
		{"down", 200 * time.Millisecond},
		{"down", 200 * time.Millisecond},
		{"down", 200 * time.Millisecond},
		{"i", 1200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, f := range frames {
		all += stripANSI(f.Text) + "\n"
	}
	for _, want := range []string{"incident loaded", "impact loaded", "would lose every replica", "every pod of scheduler is failing"} {
		if !strings.Contains(all, want) {
			t.Errorf("the drill down never showed %q", want)
		}
	}
	for _, bad := range []string{"incident: ", "impact: "} {
		if strings.Contains(all, "✓ "+bad) {
			t.Errorf("a drill down ended in an error toast %q", bad)
		}
	}
	last := stripANSI(frames[len(frames)-1].Text)
	if !strings.Contains(last, "exit code") {
		t.Errorf("the last frame is not the impact view:\n%s", last)
	}
}

// TestCastIsDeterministic guards the recording: same script, same bytes, every time.
func TestCastIsDeterministic(t *testing.T) {
	d := loadData(t)
	script := DemoScript()[:8]
	a, err := Cast(d, nil, 100, 30, script)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Cast(d, nil, 100, 30, script)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("frame counts differ: %d against %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Text != b[i].Text || a[i].At != b[i].At {
			t.Fatalf("frame %d differs between two runs", i)
		}
	}
	if len(Asciicast(a, 100, 30, "t")) == 0 {
		t.Error("the asciicast is empty")
	}
}
