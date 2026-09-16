package ui

import "time"

// Frame renders one deterministic still of the interface, at a given size and a given moment
// of the animation. Snapshot tests use it, and so does anyone reviewing the design without a
// terminal. Nothing in the drawing path reads the clock except through these two durations.
func Frame(d Data, v View, w, h int, sinceStart, sinceView time.Duration, color bool) string {
	m := newModel(d, w, h)
	m.t = NewTheme(w, color)
	m.h = h
	m.view = v
	if !m.enabled(v) && m.anyEnabled() {
		m.view = m.firstEnabled()
	}
	_, running := stepState(len(m.steps), sinceStart)
	m.collecting = running
	m.coach = sinceStart < 6*time.Second
	return m.frame(sinceStart, sinceView)
}

// FrameAfter is the still every screenshot should use: animations finished, nothing moving.
func FrameAfter(d Data, v View, w, h int, color bool) string {
	return Frame(d, v, w, h, time.Hour, time.Hour, color)
}

// Available reports whether an interactive interface can run here.
func Available() bool { return isTTY() }
