package display

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"galph/internal/state"
)

// ANSI escape codes.
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorBlue   = "\033[34m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)

// AnsiRenderer implements Renderer using raw ANSI escape codes.
// It writes to an io.Writer, defaulting to os.Stdout.
type AnsiRenderer struct {
	w io.Writer
}

// NewAnsiRenderer creates a renderer that writes to os.Stdout.
func NewAnsiRenderer() *AnsiRenderer {
	return &AnsiRenderer{w: os.Stdout}
}

// NewAnsiRendererWriter creates a renderer that writes to a custom writer.
// Useful for testing.
func NewAnsiRendererWriter(w io.Writer) *AnsiRenderer {
	return &AnsiRenderer{w: w}
}

func (a *AnsiRenderer) Init(s *DisplayState) {}

func (a *AnsiRenderer) RenderBanner(s *DisplayState) {
	a.printf("\n%s╭─ galph %s ─────────────────────────────╮%s\n", colorCyan, s.Version, colorReset)
	a.printf("%s│%s  workspace: %s\n", colorCyan, colorReset, s.Workspace)
	a.printf("%s│%s  model:     %s\n", colorCyan, colorReset, s.Model)
	a.printf("%s│%s  mode:      %s\n", colorCyan, colorReset, s.Mode)
	a.printf("%s╰──────────────────────────────────────────╯%s\n\n", colorCyan, colorReset)
}

func (a *AnsiRenderer) RenderPlanResult(s *DisplayState) {
	a.printf("\n%s  Plan: %d tasks%s\n", colorBold, len(s.Tasks), colorReset)
	a.renderTaskChecklist(s)
	a.printf("\n")
}

func (a *AnsiRenderer) RenderTaskStart(s *DisplayState) {
	task := s.Tasks[s.ActiveTaskIdx]
	a.printf("%s▶ Iteration %d/%d%s — %s\n", colorBold, s.Iteration, s.MaxIterations, colorReset, task.Description)
	a.printf("  %stask: %s%s\n", colorDim, task.ID, colorReset)
}

func (a *AnsiRenderer) RenderProgress(s *DisplayState) {
	elapsed := time.Since(s.IterStart)
	a.printf("\r  %s⟳ working... %s%s", colorYellow, FormatDuration(elapsed), colorReset)
}

func (a *AnsiRenderer) RenderPhaseChange(s *DisplayState) {
	switch s.CurrentPhase {
	case PhasePlanning:
		a.printf("%s▶ Planning phase%s\n", colorBold, colorReset)
	case PhaseTesting:
		// Shown inline via log entries, no separate header needed.
	case PhaseCommitting:
		// Shown inline via log entries.
	}
}

func (a *AnsiRenderer) RenderIterationResult(s *DisplayState) {
	// Clear the progress line.
	a.printf("\r")

	task := s.Tasks[s.ActiveTaskIdx]
	switch task.Status {
	case state.TaskComplete:
		a.printf("  %s✓ passed%s\n", colorGreen, colorReset)
	case state.TaskFailed:
		a.printf("  %s✗ failed%s: %s\n", colorRed, colorReset, task.Error)
	}

	// Task checklist
	a.renderTaskChecklist(s)

	// Cost + iteration footer
	a.printf("  %scost: %s across %d iterations%s\n",
		colorDim, FormatCost(s.TotalCost), s.Iteration, colorReset)
}

func (a *AnsiRenderer) RenderLog(s *DisplayState, entry LogEntry) {
	phaseColor := a.phaseColor(entry.Phase)

	parts := []string{entry.Action}
	if entry.Detail != "" {
		parts = append(parts, entry.Detail)
	}
	if entry.Result != "" {
		parts = append(parts, entry.Result)
	}

	a.printf("  %s[%04d]%s %s%-11s%s → %s\n",
		colorDim, entry.Seq, colorReset,
		phaseColor, entry.Phase, colorReset,
		strings.Join(parts, " · "))
}

func (a *AnsiRenderer) RenderCompletion(s *DisplayState) {
	elapsed := time.Since(s.RunStart)
	done := s.CompletedCount()
	total := len(s.Tasks)

	if s.CurrentPhase == PhaseFailed {
		a.printf("\n%s╭─ Run Stopped ───────────────────────────╮%s\n", colorRed, colorReset)
		a.printf("%s│%s  reason:    %s\n", colorRed, colorReset, s.StopReason)
		a.printf("%s│%s  progress:  %d/%d complete\n", colorRed, colorReset, done, total)
		a.printf("%s│%s  pass rate: %.1f%%\n", colorRed, colorReset, s.PassRate())
		a.printf("%s│%s  cost:      %s\n", colorRed, colorReset, FormatCost(s.TotalCost))
		a.printf("%s│%s  duration:  %s\n", colorRed, colorReset, FormatDuration(elapsed))
		a.printf("%s╰──────────────────────────────────────────╯%s\n\n", colorRed, colorReset)
	} else {
		a.printf("\n%s╭─ Run Complete ──────────────────────────╮%s\n", colorGreen, colorReset)
		a.printf("%s│%s  tasks:     %d/%d complete\n", colorGreen, colorReset, done, total)
		a.printf("%s│%s  pass rate: %.1f%%\n", colorGreen, colorReset, s.PassRate())
		a.printf("%s│%s  cost:      %s\n", colorGreen, colorReset, FormatCost(s.TotalCost))
		a.printf("%s│%s  duration:  %s\n", colorGreen, colorReset, FormatDuration(elapsed))
		a.printf("%s╰──────────────────────────────────────────╯%s\n\n", colorGreen, colorReset)

		// Show next-steps hint when PRD is done
		if s.PRDComplete && !s.HasBacklog {
			a.printf("%sWhat's next?%s Add refinements to BACKLOG.md, or run:\n", colorBold, colorReset)
			a.printf("  %sgalph refine \"describe what to fix\"%s\n\n", colorCyan, colorReset)
		}
	}
}

func (a *AnsiRenderer) Close() {}

// --- internal helpers ---

func (a *AnsiRenderer) printf(format string, args ...any) {
	fmt.Fprintf(a.w, format, args...)
}

// renderTaskChecklist prints the task list with colored status dots.
func (a *AnsiRenderer) renderTaskChecklist(s *DisplayState) {
	for i, t := range s.Tasks {
		dot, dotColor := a.taskDot(t.Status)
		marker := " "
		nameStyle := ""
		nameReset := ""
		if i == s.ActiveTaskIdx {
			marker = "▸"
			nameStyle = colorBold
			nameReset = colorReset
		}
		// Refinement tasks get a ~ prefix to visually distinguish them
		idDisplay := t.ID
		if t.Source == "backlog" || t.Source == "refine" {
			idDisplay = "~" + t.ID
		}
		a.printf("  %s %s%s%s %s%s%s%s\n",
			marker,
			dotColor, dot, colorReset,
			nameStyle, idDisplay, nameReset,
			a.taskSuffix(t))
	}
}

// taskDot returns the status dot character and its color.
func (a *AnsiRenderer) taskDot(status state.TaskStatus) (string, string) {
	switch status {
	case state.TaskComplete:
		return "●", colorGreen
	case state.TaskInProgress:
		return "●", colorYellow
	case state.TaskFailed:
		return "●", colorRed
	default:
		return "○", colorDim
	}
}

// taskSuffix returns additional info to show after the task ID.
func (a *AnsiRenderer) taskSuffix(t TaskView) string {
	// Use only the first line of the description for the checklist.
	desc := t.Description
	if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
		desc = desc[:idx]
	}
	return fmt.Sprintf("  %s%s%s", colorDim, desc, colorReset)
}

// phaseColor returns the ANSI color for a pipeline phase.
func (a *AnsiRenderer) phaseColor(p Phase) string {
	switch p {
	case PhasePlanning:
		return colorCyan
	case PhaseExecuting:
		return colorYellow
	case PhaseTesting:
		return colorBlue
	case PhaseCommitting:
		return colorGreen
	case PhaseRefining:
		return colorCyan
	default:
		return colorDim
	}
}
