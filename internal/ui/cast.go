package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The recording does not need a browser, a pseudo terminal or a clock. The interface is
// driven with scripted keys on a virtual clock, and every repaint is captured as the exact
// bytes a terminal would receive. The result is an asciicast, which any renderer can turn
// into a GIF or a video, and which is identical on every machine.

// Key is one scripted action: press a key, or just let time pass.
type Key struct {
	Press string        // "down", "right", "tab", "?", "q", or "" for a pause
	Wait  time.Duration // how long to wait after the press
}

// CastFrame is one full repaint at one instant.
type CastFrame struct {
	At   time.Duration
	Text string
}

var castKeys = map[string]tea.KeyType{
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "enter": tea.KeyEnter, "esc": tea.KeyEsc,
}

func keyMsg(k string) tea.Msg {
	if kt, ok := castKeys[k]; ok {
		return tea.KeyMsg{Type: kt}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

var castEpoch = time.Date(2026, 9, 16, 3, 20, 0, 0, time.UTC)

// Cast plays a script against the interface and returns every distinct frame with its time.
// The loader, when given, runs synchronously and its progress is replayed at a readable pace.
func Cast(d Data, l Loader, w, h int, script []Key) ([]CastFrame, error) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)

	m := newModel(d, w, h)
	m.t = NewTheme(w, true)
	clock := castEpoch
	m.now = func() time.Time { return clock }

	var frames []CastFrame
	last := ""
	capture := func() {
		text := m.frame(m.since(m.started), m.since(m.entered))
		if text != last {
			frames = append(frames, CastFrame{At: clock.Sub(castEpoch), Text: text})
			last = text
		}
	}
	apply := func(msg tea.Msg) tea.Cmd {
		next, cmd := m.Update(msg)
		m = next.(app)
		return cmd
	}
	advance := func(d time.Duration) {
		for elapsed := time.Duration(0); elapsed < d; elapsed += frameEvery {
			clock = clock.Add(frameEvery)
			apply(tickMsg(clock))
			capture()
		}
	}

	apply(tea.WindowSizeMsg{Width: w, Height: h})
	apply(tickMsg(clock))
	capture()

	if l != nil {
		m.loading, m.loaded, m.steps = true, false, nil
		var steps []Step
		data, err := l(func(what, note string) { steps = append(steps, Step{What: what, Note: note}) })
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			advance(320 * time.Millisecond)
			apply(progressMsg(s))
			capture()
		}
		advance(260 * time.Millisecond)
		if data.Actions == nil {
			data.Actions = m.data.Actions
		}
		apply(loadedMsg{data})
		capture()
	}
	advance(stepEvery * time.Duration(len(m.steps)+3))

	for _, k := range script {
		if k.Press != "" {
			cmd := apply(keyMsg(k.Press))
			capture()
			if cmd != nil {
				// an analysis started from a row: let the spinner turn, then land the result
				if msg := cmd(); msg != nil {
					switch msg.(type) {
					case whyDoneMsg, impactDoneMsg:
						advance(700 * time.Millisecond)
						apply(msg)
						capture()
					}
				}
			}
		}
		advance(k.Wait)
	}
	return frames, nil
}

// DemoScript is the walkthrough the README recording plays: the overview, the evidence behind
// a finding, an incident opened from a workload, an impact opened from a pool, the key map.
func DemoScript() []Key {
	return []Key{
		{"", 1800 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"right", 2600 * time.Millisecond},
		{"esc", 700 * time.Millisecond},
		{"up", 500 * time.Millisecond},
		{"up", 600 * time.Millisecond},
		{"w", 2800 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"right", 2600 * time.Millisecond},
		{"esc", 600 * time.Millisecond},
		{"d", 2400 * time.Millisecond},
		{"d", 600 * time.Millisecond},
		{"1", 1400 * time.Millisecond},
		{"down", 400 * time.Millisecond},
		{"down", 400 * time.Millisecond},
		{"down", 400 * time.Millisecond},
		{"down", 400 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"i", 2800 * time.Millisecond},
		{"down", 700 * time.Millisecond},
		{"right", 2400 * time.Millisecond},
		{"esc", 600 * time.Millisecond},
		{"?", 2600 * time.Millisecond},
		{"esc", 500 * time.Millisecond},
		{"e", 1800 * time.Millisecond},
		{"q", 400 * time.Millisecond},
	}
}

// StillsScript holds each screen long enough to cut a clean still out of the recording: the
// overview, the incident with the separating dimension, the impact, the key map, the evidence
// panel. The README screenshots come from it, so they always match the code.
func StillsScript() []Key {
	return []Key{
		{"", 3000 * time.Millisecond},
		{"2", 3000 * time.Millisecond},
		{"3", 3000 * time.Millisecond},
		{"?", 3000 * time.Millisecond},
		{"esc", 300 * time.Millisecond},
		{"1", 1500 * time.Millisecond},
		{"right", 3000 * time.Millisecond},
		{"esc", 300 * time.Millisecond},
		{"q", 300 * time.Millisecond},
	}
}

// Asciicast serialises frames as an asciicast v2 stream: one header line, then one event per
// repaint. Each event clears the screen and paints the whole frame, so any player shows it.
func Asciicast(frames []CastFrame, w, h int, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"version": 2, "width": %d, "height": %d, "timestamp": 1789441200, "title": %q, "env": {"TERM": "xterm-256color", "SHELL": "/bin/zsh"}}`+"\n", w, h, title)
	// hide the cursor, home, clear: a player then shows the frame and nothing else
	esc := string(rune(0x1b))
	clear := esc + "[?25l" + esc + "[H" + esc + "[2J"
	for _, f := range frames {
		text := strings.ReplaceAll(f.Text, "\n", "\r\n")
		fmt.Fprintf(&b, "[%.3f, \"o\", %s]\n", f.At.Seconds(), jsonString(clear+text))
	}
	return b.String()
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
