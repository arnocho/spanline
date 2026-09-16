package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"

	"github.com/arnocho/spanline/internal/result"
)

// RunEstate opens the cockpit: clusters, node pools, what Terraform owns, what is at risk.
func RunEstate(r *result.EstateReport) error {
	if r == nil {
		return errors.New("tui: estate report is nil")
	}
	return run(newEstateModel(r))
}

// RunWhy opens the incident view: onset, the two cohorts, the dimensions, the ranked suspects.
func RunWhy(r *result.WhyReport) error {
	if r == nil {
		return errors.New("tui: why report is nil")
	}
	return run(newWhyModel(r))
}

// RunImpact opens the pre change view: findings grouped by severity, verdict and exit code pinned.
func RunImpact(r *result.ImpactReport) error {
	if r == nil {
		return errors.New("tui: impact report is nil")
	}
	return run(newImpactModel(r))
}

// Available reports whether an interactive interface can be drawn, that is whether stdout is a TTY.
func Available() bool {
	if os.Stdout == nil {
		return false
	}
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func run(m tea.Model) error {
	if !Available() {
		return errors.New("tui: stdout is not a terminal")
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// narrativeLines renders the optional narrative. It is shown as text attached to the report,
// never as a verdict: the verdicts stay the ones the analysis computed.
func narrativeLines(n *result.Narrative, w int) []string {
	if n == nil || strings.TrimSpace(n.Text) == "" {
		return nil
	}
	meta := make([]string, 0, 3)
	if strings.TrimSpace(n.Model) != "" {
		meta = append(meta, "model "+n.Model)
	}
	if n.Unverified {
		meta = append(meta, "unverified")
	} else {
		meta = append(meta, "verified against the report")
	}
	if n.Dropped > 0 {
		meta = append(meta, fmt.Sprintf("%d dropped %s", n.Dropped, plural(n.Dropped, "sentence", "sentences")))
	}

	lines := []string{stLabel.Render(truncate("narrative ("+strings.Join(meta, ", ")+")", w))}
	lines = append(lines, wrapLines(n.Text, w, stValue)...)
	for _, c := range n.Citations {
		lines = append(lines, stFaint.Render(truncate("cites "+c, w)))
	}
	return lines
}

// wrapLines word wraps plain text to w cells and styles each resulting line.
func wrapLines(text string, w int, style lipgloss.Style) []string {
	if w < 8 {
		w = 8
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(strings.TrimSpace(text))
	out := strings.Split(wrapped, "\n")
	for i := range out {
		out[i] = style.Render(strings.TrimRight(out[i], " "))
	}
	return out
}
