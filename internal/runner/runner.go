// Package runner implements the galph autonomous loop:
// plan → execute → test → commit → repeat.
package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"galph/internal/backlog"
	"galph/internal/config"
	"galph/internal/display"
	"galph/internal/docker"
	"galph/internal/executor"
	"galph/internal/hasher"
	"galph/internal/local"
	"galph/internal/parser"
	"galph/internal/state"
)

// Runner orchestrates the galph loop.
type Runner struct {
	cfg     *config.Config
	exec    executor.Executor
	store   *state.Store
	display display.Renderer
	dstate  *display.DisplayState
	log     func(format string, args ...any)
}

// New creates a runner from config.
func New(cfg *config.Config, renderer display.Renderer, logFn func(string, ...any)) (*Runner, error) {
	// Resolve paths
	workspace := cfg.Workspace
	if !filepath.IsAbs(workspace) {
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving workspace: %w", err)
		}
		workspace = abs
	}

	// Setup state store
	galphDir := filepath.Join(workspace, ".galph")
	store, err := state.NewStore(galphDir)
	if err != nil {
		return nil, fmt.Errorf("creating state store: %w", err)
	}

	// Resolve klaudia dir
	klaudiaDir, err := cfg.ResolveKlaudiaDir()
	if err != nil {
		return nil, fmt.Errorf("finding klaudia: %w", err)
	}
	logFn("klaudia found at %s", klaudiaDir)

	// Create executor based on mode
	var exec executor.Executor
	if cfg.Mode == "local" {
		logFn("using local execution mode")
		exec, err = local.New(workspace, klaudiaDir)
		if err != nil {
			return nil, fmt.Errorf("creating local executor: %w", err)
		}
	} else {
		// Docker mode (default)
		home, _ := os.UserHomeDir()
		claudeDir := filepath.Join(home, ".claude")
		if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
			os.MkdirAll(claudeDir, 0o755)
		}

		containerName := docker.ContainerName(cfg.ProjectName, workspace)
		logFn("container name: %s", containerName)

		exec, err = docker.NewContainer(
			containerName,
			cfg.Docker.Image,
			workspace,
			klaudiaDir,
			claudeDir,
			galphDir,
			cfg.Docker.Memory,
			cfg.Docker.Network,
		)
		if err != nil {
			return nil, fmt.Errorf("creating container: %w", err)
		}
	}

	// Resolve mode string for display
	mode := cfg.Mode
	if mode == "" {
		mode = "docker"
	}

	dstate := display.NewDisplayState("0.2.0", cfg.Model, mode, workspace, cfg.MaxIterations)

	return &Runner{
		cfg:     cfg,
		exec:    exec,
		store:   store,
		display: renderer,
		dstate:  dstate,
		log:     logFn,
	}, nil
}

// Run executes the full galph loop: plan then execute.
func (r *Runner) Run() error {
	ds := r.dstate
	ds.RunStart = time.Now()

	r.display.Init(ds)
	defer r.display.Close()

	r.display.RenderBanner(ds)

	// Check PRD exists
	prdPath := r.cfg.ResolvePRD()
	if _, err := os.Stat(prdPath); os.IsNotExist(err) {
		return fmt.Errorf("PRD not found at %s\n\nTo get started:\n  galph init --describe \"what you want to build\"   # generates PRD.md\n  galph init                                        # interactive setup\n  or create PRD.md manually", prdPath)
	}

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

	// Load or create state
	st, err := r.store.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Sync persisted metrics into display state
	ds.TotalCost = st.TotalCostUSD
	ds.Iteration = st.Iteration

	// Compute current hash of planning inputs
	currentHash, err := hasher.HashPlanInputs(r.cfg.Workspace, r.cfg.ResolvePRD())
	if err != nil {
		return fmt.Errorf("hashing plan inputs: %w", err)
	}

	// Plan phase — three-way decision
	tasks, loadErr := r.store.LoadPlan()
	if loadErr != nil || len(tasks) == 0 {
		// Case 1: No existing plan — create one
		ds.CurrentPhase = display.PhasePlanning
		r.display.RenderPhaseChange(ds)
		entry := ds.AppendLog(display.PhasePlanning, "analyze PRD", "", "")
		r.display.RenderLog(ds, entry)

		tasks, err = r.planPhase(nil)
		if err != nil {
			return fmt.Errorf("plan phase: %w", err)
		}
		if err := r.store.SavePlan(tasks); err != nil {
			return fmt.Errorf("saving plan: %w", err)
		}
		st.Tasks = tasks
		st.PlanInputsHash = currentHash
		st.PlanCreatedAt = time.Now()
		r.store.SaveState(st)

		ds.Tasks = display.TasksFromState(tasks)
		entry = ds.AppendLog(display.PhasePlanning, "parse PRD", fmt.Sprintf("%d tasks", len(tasks)), "done")
		r.display.RenderLog(ds, entry)
		r.display.RenderPlanResult(ds)

	} else if st.PlanInputsHash == "" {
		// Case 2: Upgrading from older galph — adopt hash silently, resume
		r.log("storing plan input hash for future change detection")
		st.PlanInputsHash = currentHash
		r.store.SaveState(st)
		r.log("resuming with existing plan (%s)", state.Summary(tasks))
		st.Tasks = tasks
		ds.Tasks = display.TasksFromState(tasks)
		ds.SyncCountsFromTasks()
		r.display.RenderPlanResult(ds)

	} else if st.PlanInputsHash != currentHash {
		// Case 3: Plan exists but inputs changed — additive replan
		completedTasks := state.CompletedTasks(tasks)
		r.log("plan inputs changed (PRD/CLAUDE.md/.galphrc modified), replanning...")
		r.log("  preserving %d completed tasks", len(completedTasks))

		ds.CurrentPhase = display.PhasePlanning
		r.display.RenderPhaseChange(ds)
		entry := ds.AppendLog(display.PhasePlanning, "replan", fmt.Sprintf("preserving %d completed", len(completedTasks)), "")
		r.display.RenderLog(ds, entry)

		newTasks, err := r.planPhase(completedTasks)
		if err != nil {
			return fmt.Errorf("replan phase: %w", err)
		}

		// Merge: completed tasks + new tasks
		tasks = append(completedTasks, newTasks...)
		if err := r.store.SavePlan(tasks); err != nil {
			return fmt.Errorf("saving replan: %w", err)
		}
		st.Tasks = tasks
		st.PlanInputsHash = currentHash
		st.PlanCreatedAt = time.Now()
		st.ConsecutiveFailures = 0
		r.store.SaveState(st)

		ds.Tasks = display.TasksFromState(tasks)
		ds.SyncCountsFromTasks()
		entry = ds.AppendLog(display.PhasePlanning, "replan", fmt.Sprintf("%d new tasks", len(newTasks)), "done")
		r.display.RenderLog(ds, entry)
		r.display.RenderPlanResult(ds)

	} else {
		// Case 4: Plan exists and inputs unchanged — resume
		r.log("resuming with existing plan (%s)", state.Summary(tasks))
		st.Tasks = tasks
		ds.Tasks = display.TasksFromState(tasks)
		ds.SyncCountsFromTasks()
		r.display.RenderPlanResult(ds)
	}

	// Execute loop
	backlogPath := filepath.Join(r.cfg.Workspace, "BACKLOG.md")

	for i := 0; i < r.cfg.MaxIterations; i++ {
		// Check if all current tasks are done — if so, try backlog pickup
		if state.AllComplete(tasks) {
			tasks = r.pickupBacklog(tasks, backlogPath)
			if state.AllComplete(tasks) {
				// Nothing left to do
				ds.PRDComplete = state.IsPRDComplete(tasks)
				ds.HasBacklog = backlog.Exists(backlogPath)
				ds.CurrentPhase = display.PhaseComplete
				ds.Tasks = display.TasksFromState(tasks)
				r.display.RenderCompletion(ds)
				return nil
			}
			// Update display with new backlog tasks
			ds.Tasks = display.TasksFromState(tasks)
			r.display.RenderPlanResult(ds)
		}

		// Check consecutive failures
		if st.ConsecutiveFailures >= r.cfg.MaxConsecutiveFailures {
			ds.CurrentPhase = display.PhaseFailed
			ds.StopReason = fmt.Sprintf("too many consecutive failures (%d)", st.ConsecutiveFailures)
			r.display.RenderCompletion(ds)
			return fmt.Errorf("too many consecutive failures (%d), stopping", st.ConsecutiveFailures)
		}

		// Pick next task
		task := state.NextTask(tasks)
		if task == nil {
			ds.PRDComplete = state.IsPRDComplete(tasks)
			ds.HasBacklog = backlog.Exists(backlogPath)
			ds.CurrentPhase = display.PhaseComplete
			ds.Tasks = display.TasksFromState(tasks)
			r.display.RenderCompletion(ds)
			return nil
		}

		st.Iteration++
		task.Status = state.TaskInProgress
		task.Iteration = st.Iteration
		r.store.SaveState(st)

		// Update display state for this iteration
		ds.Iteration = st.Iteration
		ds.Tasks = display.TasksFromState(tasks)
		ds.ActiveTaskIdx = r.findTaskIdx(tasks, task.ID)
		ds.IterStart = time.Now()
		ds.CurrentPhase = display.PhaseExecuting

		r.display.RenderTaskStart(ds)

		entry := ds.AppendLog(display.PhaseExecuting, "klaudia", task.Description, "")
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

		// Execute iteration
		iterLog, err := r.executeIteration(st.Iteration, task)
		close(done)

		if err != nil {
			task.Status = state.TaskFailed
			task.Error = err.Error()
			st.ConsecutiveFailures++
			ds.FailCount++
			iterLog.Error = err.Error()

			entry = ds.AppendLog(display.PhaseExecuting, "klaudia", task.Description, "failed")
			r.display.RenderLog(ds, entry)
		} else {
			// Run test gate
			ds.CurrentPhase = display.PhaseTesting
			entry = ds.AppendLog(display.PhaseTesting, r.cfg.TestCommand, task.ID, "")
			r.display.RenderLog(ds, entry)

			testPassed, testErr := r.testGate()
			iterLog.TestPassed = testPassed

			if testPassed {
				task.Status = state.TaskComplete
				st.ConsecutiveFailures = 0
				ds.PassCount++

				entry = ds.AppendLog(display.PhaseTesting, "tests", task.ID, "passed")
				r.display.RenderLog(ds, entry)

				// Git commit
				if !r.cfg.DryRun {
					ds.CurrentPhase = display.PhaseCommitting
					entry = ds.AppendLog(display.PhaseCommitting, "git commit", fmt.Sprintf("iteration %d", st.Iteration), "")
					r.display.RenderLog(ds, entry)
					r.commitGate(st.Iteration, task.Description)
				}

				// Mark backlog item done if applicable
				if task.Source == "backlog" {
					r.markBacklogDone(backlogPath, task)
				}
			} else {
				task.Status = state.TaskFailed
				task.Error = fmt.Sprintf("test failed: %v", testErr)
				st.ConsecutiveFailures++
				ds.FailCount++
				iterLog.Error = task.Error

				entry = ds.AppendLog(display.PhaseTesting, "tests", task.ID, "failed")
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

	ds.CurrentPhase = display.PhaseFailed
	ds.StopReason = "max iterations reached"
	r.display.RenderCompletion(ds)
	return nil
}

// PlanOnly runs just the planning phase.
func (r *Runner) PlanOnly() error {
	ds := r.dstate

	r.display.Init(ds)
	defer r.display.Close()

	r.display.RenderBanner(ds)

	// Check PRD exists
	prdPath := r.cfg.ResolvePRD()
	if _, err := os.Stat(prdPath); os.IsNotExist(err) {
		return fmt.Errorf("PRD not found at %s\n\nTo get started:\n  galph init --describe \"what you want to build\"   # generates PRD.md\n  galph init                                        # interactive setup\n  or create PRD.md manually", prdPath)
	}

	if r.cfg.Mode != "local" {
		if err := r.ensureImage(); err != nil {
			return err
		}
	}

	r.log("starting executor...")
	if err := r.exec.Start(); err != nil {
		return fmt.Errorf("starting executor: %w", err)
	}
	defer func() {
		r.exec.Stop()
		r.exec.Remove()
	}()

	ds.CurrentPhase = display.PhasePlanning
	r.display.RenderPhaseChange(ds)
	entry := ds.AppendLog(display.PhasePlanning, "analyze PRD", "", "")
	r.display.RenderLog(ds, entry)

	tasks, err := r.planPhase(nil)
	if err != nil {
		return fmt.Errorf("plan phase: %w", err)
	}

	if err := r.store.SavePlan(tasks); err != nil {
		return fmt.Errorf("saving plan: %w", err)
	}

	// Compute and store hash
	currentHash, hashErr := hasher.HashPlanInputs(r.cfg.Workspace, r.cfg.ResolvePRD())
	if hashErr != nil {
		r.log("warning: could not hash plan inputs: %v", hashErr)
	}
	st, _ := r.store.LoadState()
	st.PlanInputsHash = currentHash
	st.PlanCreatedAt = time.Now()
	st.Tasks = tasks
	r.store.SaveState(st)

	ds.Tasks = display.TasksFromState(tasks)
	entry = ds.AppendLog(display.PhasePlanning, "parse PRD", fmt.Sprintf("%d tasks", len(tasks)), "done")
	r.display.RenderLog(ds, entry)
	r.display.RenderPlanResult(ds)
	r.log("plan saved to .galph/plan.json")
	return nil
}

// findTaskIdx returns the index of the task with the given ID, or -1.
func (r *Runner) findTaskIdx(tasks []state.Task, id string) int {
	for i, t := range tasks {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// pickupBacklog checks BACKLOG.md for pending items and appends them as tasks.
// Only picks up items when all current tasks are complete.
func (r *Runner) pickupBacklog(tasks []state.Task, backlogPath string) []state.Task {
	if !state.IsPRDComplete(tasks) {
		return tasks
	}

	items, _, err := backlog.Parse(backlogPath)
	if err != nil {
		r.log("warning: could not parse backlog: %v", err)
		return tasks
	}

	pending := backlog.PendingItems(items)
	if len(pending) == 0 {
		return tasks
	}

	r.log("found %d pending backlog items", len(pending))
	for _, item := range pending {
		id := fmt.Sprintf("refine-%03d", state.NextRefineID(tasks))
		tasks = append(tasks, state.Task{
			ID:          id,
			Description: item.Description,
			Status:      state.TaskPending,
			Source:      "backlog",
		})
	}

	// Persist the expanded task list
	r.store.SavePlan(tasks)
	return tasks
}

// markBacklogDone marks a backlog item as done in BACKLOG.md.
func (r *Runner) markBacklogDone(backlogPath string, task *state.Task) {
	items, lines, err := backlog.Parse(backlogPath)
	if err != nil {
		r.log("warning: could not parse backlog for marking done: %v", err)
		return
	}

	// Find the matching pending item by description
	for _, item := range items {
		if !item.Done && item.Description == task.Description {
			if err := backlog.MarkDone(backlogPath, lines, item.Line, task.ID, task.Iteration); err != nil {
				r.log("warning: could not mark backlog item done: %v", err)
			}
			return
		}
	}
}

func (r *Runner) ensureImage() error {
	if docker.ImageExists(r.cfg.Docker.Image) {
		r.log("image %s exists", r.cfg.Docker.Image)
		return nil
	}

	r.log("building image %s...", r.cfg.Docker.Image)
	// Look for Dockerfile in galph directory (alongside the binary)
	exe, _ := os.Executable()
	dockerfilePath := filepath.Join(filepath.Dir(exe), "Dockerfile")
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		// Try current directory
		dockerfilePath = "Dockerfile"
	}

	return docker.Build(dockerfilePath, r.cfg.Docker.Image)
}

func (r *Runner) planPhase(completedTasks []state.Task) ([]state.Task, error) {
	// Read PRD
	prdPath := r.cfg.ResolvePRD()
	prdContent, err := os.ReadFile(prdPath)
	if err != nil {
		return nil, fmt.Errorf("reading PRD: %w", err)
	}

	projectName := r.cfg.ProjectName
	if projectName == "" {
		projectName = "the project"
	}

	// List existing files in workspace so the planner knows what's there
	existingFiles := r.listWorkspaceFiles()

	// Build completed work context for additive replanning
	completedContext := ""
	if len(completedTasks) > 0 {
		var descriptions []string
		for _, t := range completedTasks {
			descriptions = append(descriptions, fmt.Sprintf("- [DONE] %s: %s", t.ID, t.Description))
		}
		completedContext = fmt.Sprintf(`
The following tasks have ALREADY been completed. Do NOT re-create these tasks.
Plan only the REMAINING work needed to fulfill the PRD.

%s

`, strings.Join(descriptions, "\n"))
	}

	wsPath := r.exec.WorkspacePath()
	prompt := fmt.Sprintf(`You are a planning agent for an autonomous coding loop called galph.

Read the PRD below and break it into discrete, independently-executable tasks.
Each task should be completable in a single klaudia session (one context window).

CRITICAL WORKSPACE RULE: Your current working directory is %s. This IS the project root.
DO NOT create a subdirectory named "%s" or any other project-name directory.
Files go directly in the current directory. For example:
  CORRECT: cmd/server/main.go
  WRONG:   %s/cmd/server/main.go

Existing files in the workspace:
%s

In each task description, include this reminder: "IMPORTANT: Create all files in the current directory (the project root). Do NOT create a project subdirectory."
%s
Output ONLY a JSON array of task objects, each with "id" and "description" fields.
The "id" should be a short slug like "task-01". The "description" should be specific
enough that a fresh klaudia instance can execute it without additional context.

PRD:
%s

Respond with ONLY the JSON array, no markdown fencing or explanation.`, wsPath, projectName, projectName, existingFiles, completedContext, string(prdContent))

	// Build klaudia command
	cmd := append(r.exec.KlaudiaCmd(),
		"--print", prompt,
		"--output-format", "stream-json",
		"--verbose",
	)

	if r.cfg.DryRun {
		r.log("  [dry-run] would run: %s", strings.Join(cmd, " "))
		return []state.Task{
			{ID: "task-01", Description: "Example task (dry run)", Status: state.TaskPending},
		}, nil
	}

	var output bytes.Buffer
	execErr := r.exec.ExecStream(cmd, &output)

	// Parse the stream-json output (even on exec error — may have partial results)
	result, parseErr := parser.ParseStreamJSON(output.String())

	// Check for auth/exec errors
	if execErr != nil {
		detail := ""
		if result != nil && result.TextOutput != "" {
			detail = result.TextOutput
		}
		if detail != "" {
			return nil, fmt.Errorf("running klaudia plan: %s", detail)
		}
		return nil, fmt.Errorf("running klaudia plan: %w", execErr)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parsing plan output: %w", parseErr)
	}
	if result.IsError {
		return nil, fmt.Errorf("klaudia plan error: %s", result.TextOutput)
	}

	// Extract JSON array from the text output
	text := strings.TrimSpace(result.TextOutput)
	// Strip markdown fences if present
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	// Find the JSON array in the text (klaudia sometimes adds preamble)
	if idx := strings.Index(text, "["); idx >= 0 {
		if end := strings.LastIndex(text, "]"); end > idx {
			text = text[idx : end+1]
		}
	}

	var tasks []state.Task
	if err := json.Unmarshal([]byte(text), &tasks); err != nil {
		return nil, fmt.Errorf("parsing task list: %w\nraw output: %s", err, text[:min(len(text), 500)])
	}

	// Set all tasks to pending
	for i := range tasks {
		tasks[i].Status = state.TaskPending
	}

	return tasks, nil
}

func (r *Runner) executeIteration(iteration int, task *state.Task) (*state.IterationLog, error) {
	iterLog := &state.IterationLog{
		Iteration: iteration,
		StartedAt: time.Now(),
		TaskID:    task.ID,
	}

	prompt := r.buildExecutionPrompt(task)
	iterLog.Prompt = prompt

	// Build klaudia command
	cmd := append(r.exec.KlaudiaCmd(),
		"--dangerously-skip-permissions",
		"--print", prompt,
		"--output-format", "stream-json",
		"--verbose",
	)

	if r.cfg.Model != "" {
		cmd = append(cmd, "--model", r.cfg.Model)
	}

	if r.cfg.DryRun {
		r.log("  [dry-run] would run: %s", strings.Join(cmd[:6], " ")+"...")
		iterLog.FinishedAt = time.Now()
		iterLog.Output = "[dry run]"
		iterLog.TestPassed = true
		return iterLog, nil
	}

	r.log("  executing klaudia...")
	var output bytes.Buffer
	err := r.exec.ExecStream(cmd, &output)

	// Parse output even if there was an error (partial results)
	result, parseErr := parser.ParseStreamJSON(output.String())
	if result != nil {
		iterLog.Output = result.TextOutput
		iterLog.ToolCalls = result.ToolCalls
		iterLog.CostUSD = result.CostUSD
		iterLog.DurationMS = result.DurationMS
	}
	iterLog.FinishedAt = time.Now()

	if err != nil {
		return iterLog, fmt.Errorf("klaudia execution: %w", err)
	}
	if parseErr != nil {
		return iterLog, fmt.Errorf("parsing output: %w", parseErr)
	}
	if result.IsError {
		return iterLog, fmt.Errorf("klaudia reported error")
	}

	return iterLog, nil
}

func (r *Runner) testGate() (bool, error) {
	if r.cfg.TestCommand == "" || r.cfg.DryRun {
		return true, nil
	}

	cmd := []string{"bash", "-c", r.cfg.TestCommand}
	output, err := r.exec.Exec(cmd, func(line string) {
		if r.cfg.Verbose {
			r.log("    test: %s", line)
		}
	})

	if err != nil {
		return false, fmt.Errorf("%s\n%s", err, output)
	}
	return true, nil
}

// buildExecutionPrompt generates the klaudia prompt for a task.
// PRD tasks get the standard "complete this task" prompt.
// Refinement tasks (backlog/refine) get a targeted "fix this issue" prompt.
func (r *Runner) buildExecutionPrompt(task *state.Task) string {
	projectName := r.cfg.ProjectName
	if projectName == "" {
		projectName = "the project"
	}

	testInfo := ""
	if r.cfg.TestCommand != "" {
		testInfo = fmt.Sprintf("\n- After making changes, verify with: %s", r.cfg.TestCommand)
	}

	existingFiles := r.listWorkspaceFiles()
	wsPath := r.exec.WorkspacePath()

	wsRule := fmt.Sprintf(`CRITICAL WORKSPACE RULE: Your current working directory (%s) IS the project root.
DO NOT create a directory called "%s" — you are already inside the project.
All files must be created relative to the current directory. For example:
  CORRECT: Write to cmd/server/main.go
  WRONG:   Write to %s/cmd/server/main.go

Before creating any file, run "ls" to confirm you are in the project root and can see existing project files.

Existing files in the workspace:
%s`, wsPath, projectName, projectName, existingFiles)

	if task.Source == "backlog" || task.Source == "refine" {
		return fmt.Sprintf(`You are working on %s. This is a refinement to an existing, working codebase.

Issue: %s

%s

Approach:
1. First, read the relevant code to understand what exists
2. Make the minimal change needed to address the issue
3. Do NOT restructure or refactor unrelated code%s
4. If you encounter an error you can't fix, explain what went wrong

When done, summarize what you changed and why.`, projectName, task.Description, wsRule, testInfo)
	}

	return fmt.Sprintf(`You are working on %s. Complete this task:

Task: %s

%s

Additional rules:
- Make minimal, focused changes%s
- If you encounter an error you can't fix, explain what went wrong

When done, summarize what you changed.`, projectName, task.Description, wsRule, testInfo)
}

// listWorkspaceFiles returns a listing of top-level files in the workspace.
func (r *Runner) listWorkspaceFiles() string {
	wsPath := r.exec.WorkspacePath()
	cmd := []string{"find", wsPath, "-maxdepth", "2", "-not", "-path", "*/.*", "-not", "-path", wsPath + "/node_modules/*"}
	if r.exec.IsRunning() {
		output, err := r.exec.Exec(cmd, nil)
		if err == nil && output != "" {
			return output
		}
	}
	// Fallback: list from host
	entries, err := os.ReadDir(r.cfg.Workspace)
	if err != nil {
		return "(unable to list)"
	}
	var parts []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			parts = append(parts, e.Name()+"/")
		} else {
			parts = append(parts, e.Name())
		}
	}
	if len(parts) == 0 {
		return "(empty workspace)"
	}
	return strings.Join(parts, "\n")
}

func (r *Runner) commitGate(iteration int, description string) {
	msg := fmt.Sprintf("galph iteration %d: %s", iteration, description)
	cmds := [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", msg, "--allow-empty"},
	}
	for _, cmd := range cmds {
		r.exec.Exec(cmd, nil)
	}
}
