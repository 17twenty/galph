package runner

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"galph/internal/backlog"
	"galph/internal/display"
	"galph/internal/state"
)

// RunRefinement executes a single refinement task.
func (r *Runner) RunRefinement(description string) error {
	return r.RunRefinements([]string{description})
}

// RunRefinements executes multiple refinement tasks in sequence.
func (r *Runner) RunRefinements(descriptions []string) error {
	ds := r.dstate
	ds.RunStart = time.Now()

	r.display.Init(ds)
	defer r.display.Close()

	r.display.RenderBanner(ds)

	// Docker-only: ensure image exists
	if r.cfg.Mode != "local" {
		if err := r.ensureImage(); err != nil {
			return err
		}
	}

	// Start executor
	r.log("starting executor...")
	if err := r.exec.Start(); err != nil {
		return fmt.Errorf("starting executor: %w", err)
	}
	defer func() {
		r.log("stopping executor...")
		r.exec.Stop()
		r.exec.Remove()
	}()

	// Load existing state
	st, err := r.store.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	tasks, _ := r.store.LoadPlan()

	ds.TotalCost = st.TotalCostUSD
	ds.Iteration = st.Iteration

	backlogPath := filepath.Join(r.cfg.Workspace, "BACKLOG.md")

	for _, desc := range descriptions {
		// Create refinement task
		id := fmt.Sprintf("refine-%03d", state.NextRefineID(tasks))
		task := state.Task{
			ID:          id,
			Description: desc,
			Status:      state.TaskPending,
			Source:      "refine",
		}
		tasks = append(tasks, task)

		// Update display
		ds.Tasks = display.TasksFromState(tasks)
		ds.ActiveTaskIdx = len(tasks) - 1

		st.Iteration++
		task.Status = state.TaskInProgress
		task.Iteration = st.Iteration
		tasks[len(tasks)-1] = task
		r.store.SaveState(st)

		ds.Iteration = st.Iteration
		ds.Tasks = display.TasksFromState(tasks)
		ds.IterStart = time.Now()
		ds.CurrentPhase = display.PhaseRefining

		r.display.RenderTaskStart(ds)

		entry := ds.AppendLog(display.PhaseRefining, "klaudia", desc, "")
		r.display.RenderLog(ds, entry)

		// Start progress ticker
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					r.display.RenderProgress(ds)
				}
			}
		}()

		// Execute
		iterLog, execErr := r.executeIteration(st.Iteration, &tasks[len(tasks)-1])
		close(done)

		if execErr != nil {
			tasks[len(tasks)-1].Status = state.TaskFailed
			tasks[len(tasks)-1].Error = execErr.Error()
			st.ConsecutiveFailures++
			ds.FailCount++
			iterLog.Error = execErr.Error()

			entry = ds.AppendLog(display.PhaseRefining, "klaudia", desc, "failed")
			r.display.RenderLog(ds, entry)
		} else {
			// Run test gate
			ds.CurrentPhase = display.PhaseTesting
			entry = ds.AppendLog(display.PhaseTesting, r.cfg.TestCommand, id, "")
			r.display.RenderLog(ds, entry)

			testPassed, testErr := r.testGate()
			iterLog.TestPassed = testPassed

			if testPassed {
				tasks[len(tasks)-1].Status = state.TaskComplete
				st.ConsecutiveFailures = 0
				ds.PassCount++

				entry = ds.AppendLog(display.PhaseTesting, "tests", id, "passed")
				r.display.RenderLog(ds, entry)

				// Git commit
				if !r.cfg.DryRun {
					ds.CurrentPhase = display.PhaseCommitting
					entry = ds.AppendLog(display.PhaseCommitting, "git commit", fmt.Sprintf("iteration %d", st.Iteration), "")
					r.display.RenderLog(ds, entry)
					r.commitGate(st.Iteration, desc)
				}

				// Record in BACKLOG.md
				backlog.AppendItem(backlogPath, desc, true, id, st.Iteration)
			} else {
				tasks[len(tasks)-1].Status = state.TaskFailed
				tasks[len(tasks)-1].Error = fmt.Sprintf("test failed: %v", testErr)
				st.ConsecutiveFailures++
				ds.FailCount++
				iterLog.Error = tasks[len(tasks)-1].Error

				entry = ds.AppendLog(display.PhaseTesting, "tests", id, "failed")
				r.display.RenderLog(ds, entry)
			}
		}

		st.TotalCostUSD += iterLog.CostUSD
		ds.TotalCost = st.TotalCostUSD
		ds.Tasks = display.TasksFromState(tasks)

		r.store.SaveIterationLog(iterLog)
		r.store.SaveState(st)
		r.store.SavePlan(tasks)

		r.display.RenderIterationResult(ds)
	}

	ds.PRDComplete = state.IsPRDComplete(tasks)
	ds.HasBacklog = backlog.Exists(backlogPath)
	ds.CurrentPhase = display.PhaseComplete
	r.display.RenderCompletion(ds)
	return nil
}

// RunRefineREPL enters interactive mode, reading refinements from stdin.
func (r *Runner) RunRefineREPL() error {
	ds := r.dstate
	ds.RunStart = time.Now()

	r.display.Init(ds)
	defer r.display.Close()

	r.display.RenderBanner(ds)

	// Docker-only: ensure image exists
	if r.cfg.Mode != "local" {
		if err := r.ensureImage(); err != nil {
			return err
		}
	}

	// Start executor
	r.log("starting executor...")
	if err := r.exec.Start(); err != nil {
		return fmt.Errorf("starting executor: %w", err)
	}
	defer func() {
		r.log("stopping executor...")
		r.exec.Stop()
		r.exec.Remove()
	}()

	// Load existing state
	st, err := r.store.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	tasks, _ := r.store.LoadPlan()

	ds.TotalCost = st.TotalCostUSD
	ds.Iteration = st.Iteration

	backlogPath := filepath.Join(r.cfg.Workspace, "BACKLOG.md")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n  refine> ")
		if !scanner.Scan() {
			break // EOF / Ctrl-D
		}
		desc := strings.TrimSpace(scanner.Text())
		if desc == "" {
			continue
		}
		if desc == "done" || desc == "exit" || desc == "quit" {
			break
		}

		// Create refinement task
		id := fmt.Sprintf("refine-%03d", state.NextRefineID(tasks))
		task := state.Task{
			ID:          id,
			Description: desc,
			Status:      state.TaskPending,
			Source:      "refine",
		}
		tasks = append(tasks, task)

		st.Iteration++
		tasks[len(tasks)-1].Status = state.TaskInProgress
		tasks[len(tasks)-1].Iteration = st.Iteration
		r.store.SaveState(st)

		ds.Iteration = st.Iteration
		ds.Tasks = display.TasksFromState(tasks)
		ds.ActiveTaskIdx = len(tasks) - 1
		ds.IterStart = time.Now()
		ds.CurrentPhase = display.PhaseRefining

		r.display.RenderTaskStart(ds)

		entry := ds.AppendLog(display.PhaseRefining, "klaudia", desc, "")
		r.display.RenderLog(ds, entry)

		// Progress ticker
		doneCh := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-doneCh:
					return
				case <-ticker.C:
					r.display.RenderProgress(ds)
				}
			}
		}()

		iterLog, execErr := r.executeIteration(st.Iteration, &tasks[len(tasks)-1])
		close(doneCh)

		if execErr != nil {
			tasks[len(tasks)-1].Status = state.TaskFailed
			tasks[len(tasks)-1].Error = execErr.Error()
			ds.FailCount++
			iterLog.Error = execErr.Error()
			entry = ds.AppendLog(display.PhaseRefining, "klaudia", desc, "failed")
			r.display.RenderLog(ds, entry)
		} else {
			ds.CurrentPhase = display.PhaseTesting
			entry = ds.AppendLog(display.PhaseTesting, r.cfg.TestCommand, id, "")
			r.display.RenderLog(ds, entry)

			testPassed, testErr := r.testGate()
			iterLog.TestPassed = testPassed

			if testPassed {
				tasks[len(tasks)-1].Status = state.TaskComplete
				ds.PassCount++
				entry = ds.AppendLog(display.PhaseTesting, "tests", id, "passed")
				r.display.RenderLog(ds, entry)

				if !r.cfg.DryRun {
					ds.CurrentPhase = display.PhaseCommitting
					entry = ds.AppendLog(display.PhaseCommitting, "git commit", fmt.Sprintf("iteration %d", st.Iteration), "")
					r.display.RenderLog(ds, entry)
					r.commitGate(st.Iteration, desc)
				}

				backlog.AppendItem(backlogPath, desc, true, id, st.Iteration)
			} else {
				tasks[len(tasks)-1].Status = state.TaskFailed
				tasks[len(tasks)-1].Error = fmt.Sprintf("test failed: %v", testErr)
				ds.FailCount++
				iterLog.Error = tasks[len(tasks)-1].Error
				entry = ds.AppendLog(display.PhaseTesting, "tests", id, "failed")
				r.display.RenderLog(ds, entry)
			}
		}

		st.TotalCostUSD += iterLog.CostUSD
		ds.TotalCost = st.TotalCostUSD
		ds.Tasks = display.TasksFromState(tasks)

		r.store.SaveIterationLog(iterLog)
		r.store.SaveState(st)
		r.store.SavePlan(tasks)

		r.display.RenderIterationResult(ds)
	}

	fmt.Println()
	return nil
}
