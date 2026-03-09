// Package display provides terminal UI for galph's progress reporting.
package display

import (
	"fmt"
	"strings"
	"time"

	"galph/internal/state"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)

// Banner prints the galph startup banner.
func Banner(version, workspace, model string) {
	fmt.Printf("\n%s╭─ galph %s ─────────────────────────────╮%s\n", colorCyan, version, colorReset)
	fmt.Printf("%s│%s  workspace: %s\n", colorCyan, colorReset, workspace)
	fmt.Printf("%s│%s  model:     %s\n", colorCyan, colorReset, model)
	fmt.Printf("%s╰──────────────────────────────────────────╯%s\n\n", colorCyan, colorReset)
}

// PlanHeader prints the planning phase header.
func PlanHeader() {
	fmt.Printf("%s▶ Planning phase%s\n", colorBold, colorReset)
}

// ReplanHeader prints the replanning phase header.
func ReplanHeader(preservedCount int) {
	fmt.Printf("%s▶ Replanning phase%s (inputs changed, preserving %d completed tasks)\n",
		colorBold, colorReset, preservedCount)
}

// PlanResult prints the parsed plan tasks.
func PlanResult(tasks []state.Task) {
	fmt.Printf("\n%s  Plan: %d tasks%s\n", colorBold, len(tasks), colorReset)
	for i, t := range tasks {
		fmt.Printf("  %s%d.%s %s\n", colorDim, i+1, colorReset, t.Description)
	}
	fmt.Println()
}

// IterationHeader prints the start of a new iteration.
func IterationHeader(iteration int, total int, task *state.Task) {
	fmt.Printf("%s▶ Iteration %d/%d%s — %s\n", colorBold, iteration, total, colorReset, task.Description)
	fmt.Printf("  %stask: %s%s\n", colorDim, task.ID, colorReset)
}

// IterationProgress prints a live status update during iteration.
func IterationProgress(elapsed time.Duration) {
	fmt.Printf("\r  %s⟳ working... %s%s", colorYellow, formatDuration(elapsed), colorReset)
}

// IterationResult prints the outcome of an iteration.
func IterationResult(log *state.IterationLog, err error) {
	fmt.Print("\r") // clear the progress line
	if err != nil {
		fmt.Printf("  %s✗ failed%s (%s): %s\n", colorRed, colorReset, formatDuration(time.Duration(log.DurationMS)*time.Millisecond), err)
	} else if log.TestPassed {
		fmt.Printf("  %s✓ passed%s (%s, $%.4f)\n", colorGreen, colorReset, formatDuration(time.Duration(log.DurationMS)*time.Millisecond), log.CostUSD)
	} else {
		fmt.Printf("  %s✗ tests failed%s (%s, $%.4f)\n", colorRed, colorReset, formatDuration(time.Duration(log.DurationMS)*time.Millisecond), log.CostUSD)
	}
}

// ProgressBar renders a visual progress bar for task completion.
func ProgressBar(tasks []state.Task) {
	total := len(tasks)
	if total == 0 {
		return
	}

	done := 0
	failed := 0
	inProgress := 0
	for _, t := range tasks {
		switch t.Status {
		case state.TaskComplete:
			done++
		case state.TaskFailed:
			failed++
		case state.TaskInProgress:
			inProgress++
		}
	}

	// Visual bar
	barWidth := 30
	filledGreen := barWidth * done / total
	filledYellow := barWidth * inProgress / total
	filledRed := barWidth * failed / total
	empty := barWidth - filledGreen - filledYellow - filledRed

	bar := strings.Repeat("█", filledGreen)
	bar += strings.Repeat("▓", filledYellow)
	bar += strings.Repeat("░", filledRed)
	bar += strings.Repeat("·", empty)

	fmt.Printf("\n  %s[%s%s%s%s%s%s%s]%s %d/%d complete",
		colorDim, colorReset,
		colorGreen, strings.Repeat("█", filledGreen), colorReset,
		colorYellow, strings.Repeat("▓", filledYellow), colorReset,
		colorReset, done, total)
	if failed > 0 {
		fmt.Printf(", %s%d failed%s", colorRed, failed, colorReset)
	}
	// Print the bar on a separate line for clarity
	_ = bar
	fmt.Println()
}

// CostSummary prints the total cost so far.
func CostSummary(cost float64, iterations int) {
	fmt.Printf("  %scost: $%.4f across %d iterations%s\n", colorDim, cost, iterations, colorReset)
}

// Completion prints the final run status.
func Completion(tasks []state.Task, totalCost float64, elapsed time.Duration) {
	done := 0
	for _, t := range tasks {
		if t.Status == state.TaskComplete {
			done++
		}
	}

	fmt.Printf("\n%s╭─ Run Complete ──────────────────────────╮%s\n", colorGreen, colorReset)
	fmt.Printf("%s│%s  tasks:    %d/%d complete\n", colorGreen, colorReset, done, len(tasks))
	fmt.Printf("%s│%s  cost:     $%.4f\n", colorGreen, colorReset, totalCost)
	fmt.Printf("%s│%s  duration: %s\n", colorGreen, colorReset, formatDuration(elapsed))
	fmt.Printf("%s╰──────────────────────────────────────────╯%s\n\n", colorGreen, colorReset)
}

// FailureSummary prints a failure summary when stopping early.
func FailureSummary(reason string, tasks []state.Task, totalCost float64) {
	done := 0
	for _, t := range tasks {
		if t.Status == state.TaskComplete {
			done++
		}
	}

	fmt.Printf("\n%s╭─ Run Stopped ───────────────────────────╮%s\n", colorRed, colorReset)
	fmt.Printf("%s│%s  reason:   %s\n", colorRed, colorReset, reason)
	fmt.Printf("%s│%s  progress: %d/%d complete\n", colorRed, colorReset, done, len(tasks))
	fmt.Printf("%s│%s  cost:     $%.4f\n", colorRed, colorReset, totalCost)
	fmt.Printf("%s╰──────────────────────────────────────────╯%s\n\n", colorRed, colorReset)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}
