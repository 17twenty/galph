// Package state manages galph's persistent state between iterations.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TaskStatus tracks the state of a single plan task.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskComplete   TaskStatus = "complete"
	TaskFailed     TaskStatus = "failed"
)

// Task represents a single unit of work from the plan.
type Task struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Iteration   int        `json:"iteration,omitempty"`
	Error       string     `json:"error,omitempty"`
	Source      string     `json:"source,omitempty"` // "prd" (default/""), "backlog", "refine"
}

// State holds the current galph run state.
type State struct {
	StartedAt           time.Time `json:"started_at"`
	LastUpdated         time.Time `json:"last_updated"`
	Iteration           int       `json:"iteration"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	TotalCostUSD        float64   `json:"total_cost_usd"`
	Tasks               []Task    `json:"tasks"`
	PlanInputsHash      string    `json:"plan_inputs_hash,omitempty"`
	PlanCreatedAt       time.Time `json:"plan_created_at,omitempty"`
}

// IterationLog records the details of a single iteration.
type IterationLog struct {
	Iteration  int       `json:"iteration"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	TaskID     string    `json:"task_id"`
	Prompt     string    `json:"prompt"`
	Output     string    `json:"output"`
	ToolCalls  []string  `json:"tool_calls"`
	CostUSD    float64   `json:"cost_usd"`
	DurationMS int       `json:"duration_ms"`
	TestPassed bool      `json:"test_passed"`
	Error      string    `json:"error,omitempty"`
}

// Store manages state persistence in a .galph directory.
type Store struct {
	dir string
}

// NewStore creates a state store at the given directory.
func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir}

	// Create directories
	for _, sub := range []string{"", "iterations"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", p, err)
		}
	}

	return s, nil
}

// LoadState reads the current state, or creates a new one.
func (s *Store) LoadState() (*State, error) {
	path := filepath.Join(s.dir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{StartedAt: time.Now(), LastUpdated: time.Now()}, nil
		}
		return nil, fmt.Errorf("reading state: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	return &state, nil
}

// SaveState persists the current state.
func (s *Store) SaveState(state *State) error {
	state.LastUpdated = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	path := filepath.Join(s.dir, "state.json")
	return os.WriteFile(path, data, 0o644)
}

// SavePlan writes the task plan.
func (s *Store) SavePlan(tasks []Task) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling plan: %w", err)
	}
	path := filepath.Join(s.dir, "plan.json")
	return os.WriteFile(path, data, 0o644)
}

// LoadPlan reads the task plan.
func (s *Store) LoadPlan() ([]Task, error) {
	path := filepath.Join(s.dir, "plan.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading plan: %w", err)
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	return tasks, nil
}

// SaveIterationLog writes a single iteration's log.
func (s *Store) SaveIterationLog(log *IterationLog) error {
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling iteration log: %w", err)
	}
	path := filepath.Join(s.dir, "iterations", fmt.Sprintf("%03d.json", log.Iteration))
	return os.WriteFile(path, data, 0o644)
}

// NextTask returns the next pending task, or nil if all done.
func NextTask(tasks []Task) *Task {
	for i := range tasks {
		if tasks[i].Status == TaskPending {
			return &tasks[i]
		}
	}
	return nil
}

// AllComplete returns true if all tasks are complete.
func AllComplete(tasks []Task) bool {
	for _, t := range tasks {
		if t.Status != TaskComplete {
			return false
		}
	}
	return len(tasks) > 0
}

// CompletedTasks returns all tasks with status "complete".
func CompletedTasks(tasks []Task) []Task {
	var completed []Task
	for _, t := range tasks {
		if t.Status == TaskComplete {
			completed = append(completed, t)
		}
	}
	return completed
}

// Summary returns a human-readable progress summary.
func Summary(tasks []Task) string {
	total := len(tasks)
	done := 0
	failed := 0
	for _, t := range tasks {
		switch t.Status {
		case TaskComplete:
			done++
		case TaskFailed:
			failed++
		}
	}
	return fmt.Sprintf("%d/%d complete, %d failed", done, total, failed)
}

// IsPRDComplete returns true if all PRD tasks (Source "" or "prd") are complete.
// Returns false if there are no PRD tasks.
func IsPRDComplete(tasks []Task) bool {
	hasPRD := false
	for _, t := range tasks {
		if t.Source == "" || t.Source == "prd" {
			hasPRD = true
			if t.Status != TaskComplete {
				return false
			}
		}
	}
	return hasPRD
}

// NextRefineID scans existing tasks for the highest refine-NNN ID and returns the next number.
func NextRefineID(tasks []Task) int {
	max := 0
	for _, t := range tasks {
		var n int
		if _, err := fmt.Sscanf(t.ID, "refine-%d", &n); err == nil {
			if n > max {
				max = n
			}
		}
	}
	return max + 1
}
