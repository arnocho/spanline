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

// revealDone reports when every row of a screen has finished arriving.
func revealDone(rows int, since time.Duration) bool {
	if rows <= 0 {
		return true
	}
	return since >= time.Duration(rows-1)*revealStep+revealFadeIn
}

// spinnerFrames is a calm spinner: no bouncing, no braille noise.
var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

func spinner(since time.Duration) string {
	return spinnerFrames[int(since/(140*time.Millisecond))%len(spinnerFrames)]
}

// step is one line of the collection screen, which doubles as an explanation of what
// spanline reads and what it deliberately does not.
type step struct {
	What string
	Note string
}

// stepState returns how many steps are done and whether one is in flight.
func stepState(n int, since time.Duration) (done int, running bool) {
	done = int(since / stepEvery)
	if done > n {
		done = n
	}
	return done, done < n
}

// pulsing reports whether a just selected row should still be highlighted.
func pulsing(since time.Duration) bool { return since < pulseFor }
