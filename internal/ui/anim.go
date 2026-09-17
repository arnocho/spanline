package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The interface animates for two reasons only: to show what the tool is reading while it
// reads it, and to make a new screen arrive in an order the eye can follow. Motion always
// finishes, and always leaves the same still frame behind.

const (
	frameEvery   = 60 * time.Millisecond // one repaint, roughly 16 per second
	revealStep   = 45 * time.Millisecond // delay between two rows arriving
	revealFadeIn = 90 * time.Millisecond // how long a row stays dim after arriving
	pulseFor     = 220 * time.Millisecond
	stepEvery    = 110 * time.Millisecond // delay between two collection steps
)

// tickMsg drives every animation. It stops as soon as nothing is moving.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// revealState says how a row should be drawn while a screen is arriving.
type revealState int

const (
	revealHidden revealState = iota
	revealDim
	revealFull
)

// reveal staggers rows: row i appears after i steps, dim at first, then full.
func reveal(i int, since time.Duration) revealState {
	at := time.Duration(i) * revealStep
	switch {
	case since < at:
		return revealHidden
	case since < at+revealFadeIn:
		return revealDim
	default:
		return revealFull
	}
}

// spinnerFrames is a calm spinner: no bouncing, no braille noise.
var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

func spinner(since time.Duration) string {
	return spinnerFrames[int(since/(140*time.Millisecond))%len(spinnerFrames)]
}

// pulsing reports whether a just selected row should still be highlighted.
func pulsing(since time.Duration) bool { return since < pulseFor }
