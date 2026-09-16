package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/cohort"
	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/estate"
	"github.com/arnocho/spanline/internal/impact"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/tfplan"
	tea "github.com/charmbracelet/bubbletea"
)

// loadData builds the same reports the binary builds, from the recorded scenarios, so the
// frames under review are the real thing rather than hand written fakes.
func loadData(t *testing.T) Data {
	t.Helper()
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	ctxs, _ := src.Contexts()
	var snaps []*model.Snapshot
	for _, c := range ctxs {
		s, err := src.Snapshot(context.Background(), c, "")
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
	}
	stateRaw, err := src.Extra("terraform/state.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := tfplan.ParseState(stateRaw)
	if err != nil {
		t.Fatal(err)
	}
	st.Path = "fixtures:terraform/state.json"
	est, err := estate.Build(snaps, []*tfplan.State{st}, estate.Options{Now: src.Now()})
	if err != nil {
		t.Fatal(err)
	}

	planRaw, err := src.Extra("terraform/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tfplan.ParsePlan(planRaw)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := impact.Plan(snaps[0], plan, []*tfplan.State{st}, "fixtures:terraform/plan.json",
		impact.Options{Now: src.Now(), TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	imp.PlanSHA = impact.PlanSHA(planRaw)

	wsrc, err := collect.NewFixtureSource("oom-rollout-blocked")
	if err != nil {
		t.Fatal(err)
	}
	wsnap, err := wsrc.Snapshot(context.Background(), "aks-prod-weu", "payments")
	if err != nil {
		t.Fatal(err)
	}
	why, err := cohort.Analyze(wsnap, cohort.Options{
		Workload: "deploy/checkout", Namespace: "payments", Now: wsrc.Now(), Window: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Data{Estate: est, Why: why, Impact: imp, Source: "fixtures: estate", Live: false}
}

// TestDumpFrames writes every screen to SPANLINE_FRAME_DIR for design review. It also holds
// the invariants that matter: a screen fits, and the answer is on it.
func TestDumpFrames(t *testing.T) {
	d := loadData(t)
	const w, h = 96, 30
	frames := map[string]string{
		"1-collect":      Frame(d, ViewOverview, w, h, 320*time.Millisecond, 0, false),
		"2-overview":     FrameAfter(d, ViewOverview, w, h, false),
		"3-incident":     FrameAfter(d, ViewIncident, w, h, false),
		"4-impact":       FrameAfter(d, ViewImpact, w, h, false),
		"5-reveal-early": Frame(d, ViewOverview, w, h, time.Hour, 120*time.Millisecond, false),
		"6-narrow":       FrameAfter(d, ViewOverview, 62, 24, false),
	}
	dir := os.Getenv("SPANLINE_FRAME_DIR")
	for name, f := range frames {
		if dir != "" {
			if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(f), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, line := range strings.Split(f, "\n") {
			if len([]rune(line)) > w {
				t.Errorf("%s: a line is wider than the terminal: %q", name, line)
			}
		}
	}
	if !strings.Contains(frames["2-overview"], "look at this first") {
		t.Error("overview does not lead with the priority list")
	}
	if !strings.Contains(frames["3-incident"], "separates them") {
		t.Error("incident does not say what separates the cohorts")
	}
	if strings.Contains(frames["3-incident"], "cause") {
		t.Error("the incident view must never print the word cause")
	}
	if !strings.Contains(frames["4-impact"], "exit code") {
		t.Error("impact does not pin the exit code")
	}
	if got := strings.Count(frames["2-overview"], "\n"); got > h {
		t.Errorf("overview is %d lines, taller than the %d line terminal", got, h)
	}
}

// TestOtherScreens covers the screens a user reaches with one keystroke: the evidence panel,
// the key map, the expanded list, and the reading animation.
func TestOtherScreens(t *testing.T) {
	d := loadData(t)
	const w, h = 96, 30

	m := newModel(d, w, h)
	m.t = NewTheme(w, false)
	m.collecting = false

	// the reading animation names what is read, and says what is never read
	collect := Frame(d, ViewOverview, w, h, 200*time.Millisecond, 0, false)
	for _, want := range []string{"reading", "nodes and pools", "secrets"} {
		if !strings.Contains(collect, want) {
			t.Errorf("the reading screen does not mention %q", want)
		}
	}

	// the key map explains every binding and what each view answers
	m.help = true
	help := m.frame(time.Hour, time.Hour)
	for _, want := range []string{"keys", "open the evidence", "what the three views answer", "incident"} {
		if !strings.Contains(help, want) {
			t.Errorf("the key map does not mention %q", want)
		}
	}

	// the evidence panel carries the fields the verdict was derived from
	m.help = false
	m.view = ViewIncident
	rows := m.filtered()
	idx := selectableIndexes(rows)
	if len(idx) == 0 {
		t.Fatal("the incident view has nothing selectable")
	}
	m.cursor[ViewIncident] = idx[len(idx)-1]
	m.detail = true
	detail := m.frame(time.Hour, time.Hour)
	if !strings.Contains(detail, "evidence") {
		t.Error("the evidence panel shows no evidence section")
	}
	if !strings.Contains(detail, "esc") {
		t.Error("the evidence panel does not say how to get back")
	}

	// expanding shows what the short screen held back
	m.detail = false
	m.view = ViewOverview
	short := m.frame(time.Hour, time.Hour)
	m.expanded = true
	long := m.frame(time.Hour, time.Hour)
	if strings.Count(long, "\n") <= strings.Count(short, "\n") {
		t.Error("pressing d did not reveal more than the short screen")
	}
	if !strings.Contains(long, "coverage") {
		t.Error("the expanded overview does not report coverage")
	}
}

// TestNoOrphanLabel guards the defect that made a label touch its value.
func TestNoOrphanLabel(t *testing.T) {
	th := NewTheme(96, false)
	line := th.KeyLine("a very long label indeed", "value", "", "")
	if !strings.Contains(line, " value") {
		t.Errorf("a long label swallowed the space before its value: %q", line)
	}
}

// TestSmallTerminal covers the sizes a split pane or a bastion window actually gives you.
// The interface must stay usable, never panic, and never draw outside the box.
func TestSmallTerminal(t *testing.T) {
	d := loadData(t)
	sizes := [][2]int{{40, 10}, {52, 14}, {62, 20}, {80, 24}, {120, 40}, {200, 60}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		for _, v := range []View{ViewOverview, ViewIncident, ViewImpact} {
			f := FrameAfter(d, v, w, h, false)
			if strings.TrimSpace(f) == "" {
				t.Fatalf("%dx%d view %d rendered nothing", w, h, v)
			}
			for _, line := range strings.Split(f, "\n") {
				if n := len([]rune(line)); n > w {
					t.Errorf("%dx%d view %d: line of %d runes overflows: %q", w, h, v, n, line)
				}
			}
			if !strings.Contains(f, "q") {
				t.Errorf("%dx%d view %d: no way out is shown", w, h, v)
			}
		}
	}
}

// TestNoDataIsHonest checks that a missing report explains itself instead of showing a blank.
func TestNoDataIsHonest(t *testing.T) {
	empty := Data{Source: "fixtures: none", Live: false}
	f := FrameAfter(empty, ViewIncident, 96, 24, false)
	if !strings.Contains(f, "spanline why") {
		t.Errorf("an empty incident view does not say how to fill it:\n%s", f)
	}
}

// press sends one key the way the terminal would, and returns the updated interface.
func press(m app, key string) app {
	var msg tea.KeyMsg
	switch key {
	case "up", "down", "left", "right", "tab", "shift+tab", "enter", "esc":
		msg = tea.KeyMsg{Type: keyTypes[key]}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, _ := m.Update(msg)
	return next.(app)
}

var keyTypes = map[string]tea.KeyType{
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "enter": tea.KeyEnter, "esc": tea.KeyEsc,
}

// TestNavigation walks the interface the way a user does on first contact.
func TestNavigation(t *testing.T) {
	d := loadData(t)
	m := newModel(d, 96, 30)
	m.t = NewTheme(96, false)
	m.collecting = false

	// down moves the selection, and the selected row is the one drawn as selected
	first := m.cursor[ViewOverview]
	m = press(m, "down")
	if m.cursor[ViewOverview] == first {
		t.Error("down did not move the selection")
	}

	// right opens the evidence, esc closes it
	m = press(m, "right")
	if !m.detail {
		t.Fatal("right did not open the evidence panel")
	}
	if !strings.Contains(m.frame(time.Hour, time.Hour), "evidence") {
		t.Error("the open panel shows no evidence")
	}
	m = press(m, "esc")
	if m.detail {
		t.Error("esc did not close the evidence panel")
	}

	// tab cycles forward through the views that hold a report
	m = press(m, "tab")
	if m.view != ViewIncident {
		t.Errorf("tab went to view %d, expected incident", m.view)
	}
	m = press(m, "tab")
	if m.view != ViewImpact {
		t.Errorf("a second tab went to view %d, expected impact", m.view)
	}
	m = press(m, "shift+tab")
	if m.view != ViewIncident {
		t.Error("shift tab did not go back")
	}

	// the number keys jump straight to a view
	m = press(m, "1")
	if m.view != ViewOverview {
		t.Error("pressing 1 did not jump to the overview")
	}

	// d expands, and expanding shows more than the short screen
	short := len(strings.Split(m.frame(time.Hour, time.Hour), "\n"))
	m = press(m, "d")
	if !m.expanded {
		t.Fatal("d did not expand")
	}
	if long := len(strings.Split(m.frame(time.Hour, time.Hour), "\n")); long <= short {
		t.Errorf("expanded screen has %d lines, short one had %d", long, short)
	}
	m = press(m, "d")

	// the filter narrows the list and esc clears it
	m = press(m, "/")
	if !m.filtering {
		t.Fatal("slash did not start a filter")
	}
	for _, r := range "redis" {
		m = press(m, string(r))
	}
	m = press(m, "enter")
	if m.filter != "redis" {
		t.Errorf("filter is %q, expected redis", m.filter)
	}
	frame := m.frame(time.Hour, time.Hour)
	if strings.Contains(frame, "payments-api") {
		t.Error("the filter kept a row that does not match")
	}
	m = press(m, "esc")
	if m.filter != "" {
		t.Error("esc did not clear the filter")
	}

	// the key map opens and closes
	m = press(m, "?")
	if !m.help {
		t.Fatal("question mark did not open the key map")
	}
	m = press(m, "?")
	if m.help {
		t.Error("question mark did not close the key map")
	}

	// the coach line disappears on the first key, it is a hint and not furniture
	if m.coach {
		t.Error("the coach line survived a key press")
	}
}
