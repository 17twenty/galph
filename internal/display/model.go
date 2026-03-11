// Package display provides terminal UI for galph's progress reporting.
//
// The display layer is built around a DisplayState value (pure data) and a
// Renderer interface.  The runner mutates DisplayState as work progresses and
// calls Renderer methods at lifecycle boundaries.  This separation lets us
// swap the ANSI renderer for a full TUI (e.g. bubbletea) later without
// touching business logic.
package display

import (
	"time"

	"galph/internal/state"
)

// Phase identifies which stage of the loop is active.
type Phase string

const (
	PhaseIdle       Phase = "idle"
	PhasePlanning   Phase = "planning"
	PhaseExecuting  Phase = "executing"
	PhaseTesting    Phase = "testing"
	PhaseCommitting Phase = "committing"
	PhaseComplete   Phase = "complete"
	PhaseFailed     Phase = "failed"
	PhaseRefining   Phase = "refining"
)

// TaskView is a display-oriented snapshot of a single plan task.
type TaskView struct {
	ID          string
	Description string
	Status      state.TaskStatus
	Iteration   int
	Error       string
	Source      string // "prd" (default/""), "backlog", "refine"
}

// LogEntry is a single line in the structured pipeline log.
type LogEntry struct {
	Seq    int
	Phase  Phase
	Action string
	Detail string
	Result string
	Time   time.Time
}

// DisplayState is the single source of truth for rendering.
// The runner builds and mutates it; the renderer reads it.
type DisplayState struct {
	// System info (set once at startup)
	Version   string
	Model     string
	Mode      string // "docker" or "local"
	Workspace string

	// Task checklist
	Tasks []TaskView

	// Active work
	CurrentPhase  Phase
	ActiveTaskIdx int // index into Tasks, -1 if none
	Iteration     int
	MaxIterations int
	IterStart     time.Time

	// Metrics
	TotalCost float64
	RunStart  time.Time
	PassCount int
	FailCount int

	// Structured log (append-only)
	Log []LogEntry

	// Terminal state
	StopReason  string // non-empty when run is stopped/complete
	PRDComplete bool   // true when all PRD tasks are done
	HasBacklog  bool   // true when BACKLOG.md exists
}

// NewDisplayState returns an initialized DisplayState with sensible defaults.
func NewDisplayState(version, model, mode, workspace string, maxIterations int) *DisplayState {
	return &DisplayState{
		Version:       version,
		Model:         model,
		Mode:          mode,
		Workspace:     workspace,
		MaxIterations: maxIterations,
		RunStart:      time.Now(),
		ActiveTaskIdx: -1,
	}
}

// TasksFromState converts state.Task slices to display TaskViews.
func TasksFromState(tasks []state.Task) []TaskView {
	views := make([]TaskView, len(tasks))
	for i, t := range tasks {
		views[i] = TaskView{
			ID:          t.ID,
			Description: t.Description,
			Status:      t.Status,
			Iteration:   t.Iteration,
			Error:       t.Error,
			Source:      t.Source,
		}
	}
	return views
}

// CompletedCount returns how many tasks are complete.
func (ds *DisplayState) CompletedCount() int {
	n := 0
	for _, t := range ds.Tasks {
		if t.Status == state.TaskComplete {
			n++
		}
	}
	return n
}

// PassRate returns the pass rate as a percentage, or 0 if no results yet.
func (ds *DisplayState) PassRate() float64 {
	total := ds.PassCount + ds.FailCount
	if total == 0 {
		return 0
	}
	return float64(ds.PassCount) / float64(total) * 100
}

// SyncCountsFromTasks sets PassCount and FailCount based on current task statuses.
// Call this when resuming a run with pre-existing task results.
func (ds *DisplayState) SyncCountsFromTasks() {
	ds.PassCount = 0
	ds.FailCount = 0
	for _, t := range ds.Tasks {
		switch t.Status {
		case state.TaskComplete:
			ds.PassCount++
		case state.TaskFailed:
			ds.FailCount++
		}
	}
}

// AppendLog adds a log entry with an auto-incremented sequence number.
func (ds *DisplayState) AppendLog(phase Phase, action, detail, result string) LogEntry {
	entry := LogEntry{
		Seq:    len(ds.Log) + 1,
		Phase:  phase,
		Action: action,
		Detail: detail,
		Result: result,
		Time:   time.Now(),
	}
	ds.Log = append(ds.Log, entry)
	return entry
}
