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
	cfg  *config.Config
	exec executor.Executor
	store *state.Store
	log   func(format string, args ...any)
}

// New creates a runner from config.
func New(cfg *config.Config, logFn func(string, ...any)) (*Runner, error) {
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

	return &Runner{
		cfg:   cfg,
		exec:  exec,
		store: store,
		log:   logFn,
	}, nil
}

// Run executes the full galph loop: plan then execute.
func (r *Runner) Run() error {
	runStart := time.Now()
	display.Banner("0.2.0", r.cfg.Workspace, r.cfg.Model)

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

	// Compute current hash of planning inputs
	currentHash, err := hasher.HashPlanInputs(r.cfg.Workspace, r.cfg.ResolvePRD())
	if err != nil {
		return fmt.Errorf("hashing plan inputs: %w", err)
	}

	// Plan phase — three-way decision
	tasks, loadErr := r.store.LoadPlan()
	if loadErr != nil || len(tasks) == 0 {
		// Case 1: No existing plan — create one
		display.PlanHeader()
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
		display.PlanResult(tasks)

	} else if st.PlanInputsHash == "" {
		// Case 2: Upgrading from older galph — adopt hash silently, resume
		r.log("storing plan input hash for future change detection")
		st.PlanInputsHash = currentHash
		r.store.SaveState(st)
		r.log("resuming with existing plan (%s)", state.Summary(tasks))
		st.Tasks = tasks
		display.ProgressBar(tasks)

	} else if st.PlanInputsHash != currentHash {
		// Case 3: Plan exists but inputs changed — additive replan
		completedTasks := state.CompletedTasks(tasks)
		r.log("plan inputs changed (PRD/CLAUDE.md/.galphrc modified), replanning...")
		r.log("  preserving %d completed tasks", len(completedTasks))
		display.ReplanHeader(len(completedTasks))

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
		display.PlanResult(newTasks)

	} else {
		// Case 4: Plan exists and inputs unchanged — resume
		r.log("resuming with existing plan (%s)", state.Summary(tasks))
		st.Tasks = tasks
		display.ProgressBar(tasks)
	}

	// Execute loop
	for i := 0; i < r.cfg.MaxIterations; i++ {
		// Check if all done
		if state.AllComplete(tasks) {
			display.Completion(tasks, st.TotalCostUSD, time.Since(runStart))
			return nil
		}

		// Check consecutive failures
		if st.ConsecutiveFailures >= r.cfg.MaxConsecutiveFailures {
			display.FailureSummary(
				fmt.Sprintf("too many consecutive failures (%d)", st.ConsecutiveFailures),
				tasks, st.TotalCostUSD,
			)
			return fmt.Errorf("too many consecutive failures (%d), stopping", st.ConsecutiveFailures)
		}

		// Pick next task
		task := state.NextTask(tasks)
		if task == nil {
			display.Completion(tasks, st.TotalCostUSD, time.Since(runStart))
			return nil
		}

		st.Iteration++
		task.Status = state.TaskInProgress
		task.Iteration = st.Iteration
		r.store.SaveState(st)

		display.IterationHeader(st.Iteration, r.cfg.MaxIterations, task)

		// Start progress ticker
		iterStart := time.Now()
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					display.IterationProgress(time.Since(iterStart))
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
			iterLog.Error = err.Error()
			display.IterationResult(iterLog, err)
		} else {
			// Run test gate
			testPassed, testErr := r.testGate()
			iterLog.TestPassed = testPassed

			if testPassed {
				task.Status = state.TaskComplete
				st.ConsecutiveFailures = 0
				display.IterationResult(iterLog, nil)

				// Git commit
				if !r.cfg.DryRun {
					r.commitGate(st.Iteration, task.Description)
				}
			} else {
				task.Status = state.TaskFailed
				task.Error = fmt.Sprintf("test failed: %v", testErr)
				st.ConsecutiveFailures++
				iterLog.Error = task.Error
				display.IterationResult(iterLog, fmt.Errorf("tests failed: %v", testErr))
			}
		}

		st.TotalCostUSD += iterLog.CostUSD
		r.store.SaveIterationLog(iterLog)
		r.store.SaveState(st)
		r.store.SavePlan(tasks)

		display.ProgressBar(tasks)
		display.CostSummary(st.TotalCostUSD, st.Iteration)
	}

	display.FailureSummary("max iterations reached", tasks, st.TotalCostUSD)
	return nil
}

// PlanOnly runs just the planning phase.
func (r *Runner) PlanOnly() error {
	display.Banner("0.2.0", r.cfg.Workspace, r.cfg.Model)

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

	display.PlanHeader()
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

	display.PlanResult(tasks)
	r.log("plan saved to .galph/plan.json")
	return nil
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

	projectName := r.cfg.ProjectName
	if projectName == "" {
		projectName = "the project"
	}

	testInfo := ""
	if r.cfg.TestCommand != "" {
		testInfo = fmt.Sprintf("\n- After making changes, verify with: %s", r.cfg.TestCommand)
	}

	// List existing files so klaudia sees what's already there
	existingFiles := r.listWorkspaceFiles()

	wsPath := r.exec.WorkspacePath()
	prompt := fmt.Sprintf(`You are working on %s. Complete this task:

Task: %s

CRITICAL WORKSPACE RULE: Your current working directory (%s) IS the project root.
DO NOT create a directory called "%s" — you are already inside the project.
All files must be created relative to the current directory. For example:
  CORRECT: Write to cmd/server/main.go
  WRONG:   Write to %s/cmd/server/main.go

Before creating any file, run "ls" to confirm you are in the project root and can see existing project files.

Existing files in the workspace:
%s

Additional rules:
- Make minimal, focused changes%s
- If you encounter an error you can't fix, explain what went wrong

When done, summarize what you changed.`, projectName, task.Description, wsPath, projectName, projectName, existingFiles, testInfo)

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
