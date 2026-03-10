// galph — Autonomous driver for Klaudia.
// Loops klaudia in a Docker container to work through a PRD.
//
// Usage:
//
//	galph new <name>        Scaffold a new galph project
//	galph run [flags]       Run the full plan+execute loop
//	galph plan [flags]      Run only the planning phase
//	galph status            Show current run state
//	galph logs [N]          Show iteration logs
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"galph/internal/config"
	"galph/internal/hasher"
	"galph/internal/runner"
	"galph/internal/state"
)

var version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "new":
		cmdNew(os.Args[2:])
	case "init":
		cmdInit(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "plan":
		cmdPlan(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "logs":
		cmdLogs(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("galph %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `galph %s — Autonomous driver for Klaudia

Usage:
  galph new <name>        Scaffold a new galph project in a new directory
  galph init [flags]      Add galph to an existing project (current directory)
  galph run [flags]       Run the full plan+execute loop
  galph plan [flags]      Run only the planning phase
  galph status [flags]    Show current run state
  galph logs [N]          Show iteration log (latest or specific)

New:
  Creates a project directory with PRD.md, CLAUDE.md, .galphrc, and git init.
  Then cd into the directory and run 'galph plan' or 'galph run'.

Init:
  Adds galph to the current directory. Detects existing project setup
  (languages, build tools) and generates .galphrc + CLAUDE.md.
  --name NAME             Project name (default: directory name)
  --describe "..."        Generate PRD.md from this description
  If no PRD.md exists and --describe is not given, prompts interactively.

Run flags:
  --workspace PATH        Path to workspace (default: . current directory)
  --prd PATH              Path to PRD file (relative to workspace)
  --max-iterations N      Maximum iterations (default: 50)
  --model MODEL           Claude model to use (default: claude-sonnet-4-6)
  --test-command CMD      Test command to gate iterations
  --local                 Run klaudia locally (no Docker container)
  --mode MODE             Execution mode: "docker" (default) or "local"
  --dry-run               Simulate without executing
  --verbose               Show detailed output
  --image IMAGE           Docker image name (default: galph-klaudia)

Execution modes:
  docker (default)        Runs klaudia inside a Docker container with workspace
                          mounted at /workspace. Requires Docker.
  local                   Runs klaudia directly on the host. Required for
                          platform-specific toolchains (Swift/Xcode, etc.)
                          that can't run in a Linux container.

Config:
  Place a .galphrc (JSON) in the project directory to set defaults.
  CLI flags override .galphrc values. Set "mode": "local" in .galphrc
  to default to local execution.
`, version)
}

func parseRunFlags(args []string) *config.Config {
	cfg, err := config.Load(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		cfg = config.DefaultConfig()
	}

	var localFlag bool

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.StringVar(&cfg.Workspace, "workspace", cfg.Workspace, "")
	fs.StringVar(&cfg.PRD, "prd", cfg.PRD, "")
	fs.IntVar(&cfg.MaxIterations, "max-iterations", cfg.MaxIterations, "")
	fs.IntVar(&cfg.MaxConsecutiveFailures, "max-failures", cfg.MaxConsecutiveFailures, "")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "")
	fs.StringVar(&cfg.TestCommand, "test-command", cfg.TestCommand, "")
	fs.BoolVar(&localFlag, "local", false, "")
	fs.StringVar(&cfg.Mode, "mode", cfg.Mode, "")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "")
	fs.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "")
	fs.StringVar(&cfg.Docker.Image, "image", cfg.Docker.Image, "")
	fs.StringVar(&cfg.Docker.Memory, "memory", cfg.Docker.Memory, "")
	fs.StringVar(&cfg.Docker.Network, "network", cfg.Docker.Network, "")
	fs.StringVar(&cfg.Docker.KlaudiaDir, "klaudia-dir", cfg.Docker.KlaudiaDir, "")
	fs.Parse(args)

	// --local is shorthand for --mode local
	if localFlag {
		cfg.Mode = "local"
	}

	return cfg
}

func cmdNew(args []string) {
	if len(args) == 0 {
		fatal("usage: galph new <project-name>")
	}
	name := args[0]

	// Validate name
	if strings.ContainsAny(name, "/\\. ") {
		fatal("project name must be a simple directory name (no slashes, dots, or spaces)")
	}

	// Check if directory already exists
	if _, err := os.Stat(name); err == nil {
		fatal("directory %q already exists", name)
	}

	fmt.Printf("Creating galph project: %s\n", name)

	// Create project directory
	if err := os.MkdirAll(name, 0o755); err != nil {
		fatal("creating directory: %v", err)
	}

	// Create PRD.md
	prdContent := fmt.Sprintf(`# %s — Product Requirements Document

## Overview

Describe what this project should do.

## Goals

1.

## Requirements

### Phase 1

- [ ]

## Non-Goals

-

## Technical Notes

-
`, name)
	if err := os.WriteFile(filepath.Join(name, "PRD.md"), []byte(prdContent), 0o644); err != nil {
		fatal("creating PRD.md: %v", err)
	}
	fmt.Println("  created PRD.md")

	// Create CLAUDE.md
	claudeContent := fmt.Sprintf(`# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project: %s

This project is managed by galph, an autonomous coding loop driver.

## CRITICAL: Workspace Root

The current working directory IS the project root. DO NOT create a subdirectory named "%s".
All files (go.mod, cmd/, internal/, etc.) belong directly in this directory.

## Build & Test

To verify changes:
`+"```"+`bash
# Add your build command here (update .galphrc test_command too)
# Example: npm test, go test ./..., make test
`+"```"+`

## Conventions

- Make minimal, focused changes per task
- Run build/test verification before considering work complete
- Commit after each successful change
`, name, name)
	if err := os.WriteFile(filepath.Join(name, "CLAUDE.md"), []byte(claudeContent), 0o644); err != nil {
		fatal("creating CLAUDE.md: %v", err)
	}
	fmt.Println("  created CLAUDE.md")

	// Create .galphrc
	cfg := &config.Config{
		ProjectName:            name,
		Workspace:              ".",
		PRD:                    "PRD.md",
		MaxIterations:          50,
		MaxConsecutiveFailures: 3,
		TestCommand:            "",
		Model:                  "claude-sonnet-4-6",
		Docker: config.DockerConfig{
			Image:   "galph-klaudia",
			Memory:  "4g",
			Network: "host",
		},
	}
	if err := config.WriteRC(name, cfg); err != nil {
		fatal("creating .galphrc: %v", err)
	}
	fmt.Println("  created .galphrc")

	// Create .gitignore
	gitignore := `.galph/
`
	if err := os.WriteFile(filepath.Join(name, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		fatal("creating .gitignore: %v", err)
	}
	fmt.Println("  created .gitignore")

	// Git init
	cmd := exec.Command("git", "init", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git init failed: %v\n", err)
	} else {
		// Initial commit
		gitCmd := exec.Command("git", "-C", name, "add", "-A")
		gitCmd.Run()
		gitCmd = exec.Command("git", "-C", name, "commit", "-m", "galph: scaffold project")
		gitCmd.Stdout = os.Stdout
		gitCmd.Stderr = os.Stderr
		gitCmd.Run()
	}

	fmt.Printf("\nProject %s is ready!\n", name)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. cd %s\n", name)
	fmt.Printf("  2. Edit PRD.md with your requirements\n")
	fmt.Printf("  3. Edit .galphrc to set test_command\n")
	fmt.Printf("  4. galph plan          # break PRD into tasks\n")
	fmt.Printf("  5. galph run           # execute the plan\n")
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "Project name (default: directory name)")
	describe := fs.String("describe", "", "Generate PRD.md from this description")
	fs.Parse(args)

	dir := "."

	// Detect project name
	projectName := *name
	if projectName == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			fatal("resolving directory: %v", err)
		}
		projectName = filepath.Base(abs)
	}

	// Check if already initialized
	if _, err := os.Stat(filepath.Join(dir, ".galphrc")); err == nil {
		fatal("galph already initialized (found .galphrc)")
	}

	fmt.Printf("Initializing galph in current directory: %s\n", projectName)

	// Detect existing project characteristics
	detected := detectProject(dir)
	fmt.Printf("  detected: %s\n", detected.summary())

	// Create .galphrc with detected settings
	cfg := &config.Config{
		ProjectName:            projectName,
		Workspace:              ".",
		PRD:                    "PRD.md",
		MaxIterations:          50,
		MaxConsecutiveFailures: 3,
		TestCommand:            detected.testCommand,
		Model:                  "claude-sonnet-4-6",
		Docker: config.DockerConfig{
			Image:   "galph-klaudia",
			Memory:  "4g",
			Network: "host",
		},
	}
	if err := config.WriteRC(dir, cfg); err != nil {
		fatal("creating .galphrc: %v", err)
	}
	fmt.Println("  created .galphrc")
	if detected.testCommand != "" {
		fmt.Printf("    test_command: %s\n", detected.testCommand)
	}

	// Create CLAUDE.md if it doesn't exist
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); os.IsNotExist(err) {
		claudeContent := fmt.Sprintf(`# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project: %s

This project is managed by galph, an autonomous coding loop driver.

## CRITICAL: Workspace Root

The current working directory IS the project root. DO NOT create a subdirectory named "%s".
All files belong directly in this directory.

## Build & Test

`+"```"+`bash
%s
`+"```"+`

## Conventions

- Make minimal, focused changes per task
- Run build/test verification before considering work complete
- Commit after each successful change
`, projectName, projectName, detected.buildTestInfo())
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claudeContent), 0o644); err != nil {
			fatal("creating CLAUDE.md: %v", err)
		}
		fmt.Println("  created CLAUDE.md")
	} else {
		fmt.Println("  CLAUDE.md already exists, skipping")
	}

	// Append .galph/ to .gitignore if it exists
	appendGitignore(dir)

	// Handle PRD.md
	if _, err := os.Stat(filepath.Join(dir, "PRD.md")); os.IsNotExist(err) {
		if *describe != "" {
			// Generate PRD from description
			prd := generatePRD(projectName, *describe)
			if err := os.WriteFile(filepath.Join(dir, "PRD.md"), []byte(prd), 0o644); err != nil {
				fatal("creating PRD.md: %v", err)
			}
			fmt.Println("  created PRD.md (from --describe)")
		} else if isInteractive() {
			// Interactive prompt
			fmt.Println()
			fmt.Println("No PRD.md found. Describe what you want to build (press Enter twice to finish):")
			fmt.Print("> ")
			description := readMultiline()
			if description != "" {
				prd := generatePRD(projectName, description)
				if err := os.WriteFile(filepath.Join(dir, "PRD.md"), []byte(prd), 0o644); err != nil {
					fatal("creating PRD.md: %v", err)
				}
				fmt.Println("  created PRD.md")
			} else {
				fmt.Println("  skipped PRD.md (no description provided)")
				fmt.Println("  create PRD.md manually before running 'galph plan'")
			}
		} else {
			fmt.Println("  no PRD.md found — create one manually or use 'galph init --describe \"...\"'")
		}
	} else {
		fmt.Println("  PRD.md already exists, using it")
	}

	fmt.Printf("\nProject %s initialized for galph!\n", projectName)
	fmt.Printf("\nNext steps:\n")
	if _, err := os.Stat(filepath.Join(dir, "PRD.md")); os.IsNotExist(err) {
		fmt.Printf("  1. Create PRD.md with your requirements\n")
		fmt.Printf("  2. galph plan          # break PRD into tasks\n")
		fmt.Printf("  3. galph run           # execute the plan\n")
	} else {
		fmt.Printf("  1. Review PRD.md and .galphrc\n")
		fmt.Printf("  2. galph plan          # break PRD into tasks\n")
		fmt.Printf("  3. galph run           # execute the plan\n")
	}
}

// projectInfo holds detected project characteristics.
type projectInfo struct {
	languages   []string
	testCommand string
	hasGit      bool
}

func (p *projectInfo) summary() string {
	parts := []string{}
	if len(p.languages) > 0 {
		parts = append(parts, strings.Join(p.languages, ", "))
	}
	if p.hasGit {
		parts = append(parts, "git repo")
	}
	if len(parts) == 0 {
		return "empty project"
	}
	return strings.Join(parts, " | ")
}

func (p *projectInfo) buildTestInfo() string {
	if p.testCommand != "" {
		return p.testCommand
	}
	return "# Add your build/test commands here"
}

// detectProject scans the directory for known project markers.
func detectProject(dir string) *projectInfo {
	info := &projectInfo{}

	// Check for git
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		info.hasGit = true
	}

	// Swift
	if _, err := os.Stat(filepath.Join(dir, "Package.swift")); err == nil {
		info.languages = append(info.languages, "Swift")
		info.testCommand = "swift build && swift test"
	}

	// Go
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		info.languages = append(info.languages, "Go")
		info.testCommand = "go build ./... && go test ./... && go vet ./..."
	}

	// Node.js
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		info.languages = append(info.languages, "Node.js")
		if info.testCommand == "" {
			// Check if there's a test script
			if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
				if strings.Contains(string(data), `"test"`) {
					info.testCommand = "npm test"
				}
			}
		}
	}

	// Python
	for _, f := range []string{"setup.py", "pyproject.toml", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			info.languages = append(info.languages, "Python")
			if info.testCommand == "" {
				info.testCommand = "python -m pytest"
			}
			break
		}
	}

	// Rust
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		info.languages = append(info.languages, "Rust")
		if info.testCommand == "" {
			info.testCommand = "cargo build && cargo test"
		}
	}

	// Makefile
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
		if info.testCommand == "" {
			info.testCommand = "make test"
		}
	}

	// Flutter/Dart
	if _, err := os.Stat(filepath.Join(dir, "pubspec.yaml")); err == nil {
		info.languages = append(info.languages, "Dart/Flutter")
		if info.testCommand == "" {
			info.testCommand = "dart test"
		}
	}

	// C/C++ (CMake)
	if _, err := os.Stat(filepath.Join(dir, "CMakeLists.txt")); err == nil {
		info.languages = append(info.languages, "C/C++")
		if info.testCommand == "" {
			info.testCommand = "cmake --build build && ctest --test-dir build"
		}
	}

	return info
}

// generatePRD creates a structured PRD from a user description.
func generatePRD(name, description string) string {
	return fmt.Sprintf(`# %s — Product Requirements Document

## Overview

%s

## Goals

1. Implement the functionality described above
2. Include appropriate tests
3. Follow language/framework best practices

## Requirements

### Phase 1 — Core Implementation

- [ ] Set up project structure and dependencies
- [ ] Implement core functionality as described
- [ ] Add unit tests for all components
- [ ] Ensure build and tests pass cleanly

## Technical Notes

- Follow existing project conventions if any
- Use standard library where possible
- Keep changes minimal and focused
`, name, description)
}

// isInteractive returns true if stdin is a terminal.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// readMultiline reads lines from stdin until an empty line is entered.
func readMultiline() string {
	scanner := bufio.NewScanner(os.Stdin)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && len(lines) > 0 {
			break
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// appendGitignore adds .galph/ to .gitignore if not already present.
func appendGitignore(dir string) {
	gitignorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			os.WriteFile(gitignorePath, []byte(".galph/\n"), 0o644)
			fmt.Println("  created .gitignore")
			return
		}
		return
	}
	if strings.Contains(string(content), ".galph") {
		return
	}
	// Append
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString("\n.galph/\n")
	fmt.Println("  updated .gitignore (added .galph/)")
}

func cmdRun(args []string) {
	cfg := parseRunFlags(args)
	logFn := makeLogger(cfg.Verbose)

	r, err := runner.New(cfg, logFn)
	if err != nil {
		fatal("setup: %v", err)
	}

	if err := r.Run(); err != nil {
		fatal("run: %v", err)
	}
}

func cmdPlan(args []string) {
	cfg := parseRunFlags(args)
	logFn := makeLogger(cfg.Verbose)

	r, err := runner.New(cfg, logFn)
	if err != nil {
		fatal("setup: %v", err)
	}

	if err := r.PlanOnly(); err != nil {
		fatal("plan: %v", err)
	}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "")
	fs.Parse(args)

	galphDir := filepath.Join(*workspace, ".galph")
	store, err := state.NewStore(galphDir)
	if err != nil {
		fatal("opening state: %v", err)
	}

	st, err := store.LoadState()
	if err != nil {
		fatal("loading state: %v", err)
	}

	tasks, _ := store.LoadPlan()

	fmt.Printf("galph status\n")
	fmt.Printf("  started:     %s\n", st.StartedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("  updated:     %s\n", st.LastUpdated.Format("2006-01-02 15:04:05"))
	fmt.Printf("  iteration:   %d\n", st.Iteration)
	fmt.Printf("  failures:    %d consecutive\n", st.ConsecutiveFailures)
	fmt.Printf("  total cost:  $%.4f\n", st.TotalCostUSD)

	if st.PlanInputsHash != "" {
		hashDisplay := st.PlanInputsHash
		if len(hashDisplay) > 16 {
			hashDisplay = hashDisplay[:8] + "..." + hashDisplay[len(hashDisplay)-8:]
		}
		fmt.Printf("  plan hash:   %s\n", hashDisplay)
	}
	if !st.PlanCreatedAt.IsZero() {
		fmt.Printf("  planned at:  %s\n", st.PlanCreatedAt.Format("2006-01-02 15:04:05"))
	}

	// Check if inputs changed since last plan
	if st.PlanInputsHash != "" {
		// Resolve PRD path relative to workspace
		cfg, _ := config.Load(*workspace)
		prdPath := cfg.ResolvePRD()
		currentHash, err := hasher.HashPlanInputs(*workspace, prdPath)
		if err == nil && currentHash != st.PlanInputsHash {
			fmt.Printf("  \033[33m⚠ plan inputs changed — next 'galph run' will replan\033[0m\n")
		}
	}

	if len(tasks) > 0 {
		fmt.Printf("  progress:    %s\n", state.Summary(tasks))
		fmt.Println()
		for _, t := range tasks {
			marker := " "
			switch t.Status {
			case state.TaskComplete:
				marker = "✓"
			case state.TaskInProgress:
				marker = "→"
			case state.TaskFailed:
				marker = "✗"
			}
			fmt.Printf("  [%s] %s: %s\n", marker, t.ID, t.Description)
			if t.Error != "" {
				fmt.Printf("      error: %s\n", t.Error)
			}
		}
	} else {
		fmt.Printf("  progress:    no plan yet\n")
	}
}

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	workspace := fs.String("workspace", ".", "")
	fs.Parse(args)

	galphDir := filepath.Join(*workspace, ".galph")

	// Determine which iteration to show
	var iterNum int
	if fs.NArg() > 0 {
		n, err := strconv.Atoi(fs.Arg(0))
		if err != nil {
			fatal("invalid iteration number: %s", fs.Arg(0))
		}
		iterNum = n
	} else {
		// Find latest
		store, err := state.NewStore(galphDir)
		if err != nil {
			fatal("opening state: %v", err)
		}
		st, err := store.LoadState()
		if err != nil {
			fatal("loading state: %v", err)
		}
		iterNum = st.Iteration
	}

	logPath := filepath.Join(galphDir, "iterations", fmt.Sprintf("%03d.json", iterNum))
	data, err := os.ReadFile(logPath)
	if err != nil {
		fatal("reading log: %v", err)
	}

	var log state.IterationLog
	json.Unmarshal(data, &log)

	fmt.Printf("Iteration %d\n", log.Iteration)
	fmt.Printf("  task:     %s\n", log.TaskID)
	fmt.Printf("  started:  %s\n", log.StartedAt.Format("15:04:05"))
	fmt.Printf("  finished: %s\n", log.FinishedAt.Format("15:04:05"))
	fmt.Printf("  duration: %dms\n", log.DurationMS)
	fmt.Printf("  cost:     $%.4f\n", log.CostUSD)
	fmt.Printf("  tests:    %v\n", log.TestPassed)
	fmt.Printf("  tools:    %s\n", formatTools(log.ToolCalls))
	if log.Error != "" {
		fmt.Printf("  error:    %s\n", log.Error)
	}
	fmt.Println()
	fmt.Println("--- Output ---")
	fmt.Println(log.Output)
}

func formatTools(tools []string) string {
	if len(tools) == 0 {
		return "(none)"
	}
	// Count tool uses
	counts := make(map[string]int)
	for _, t := range tools {
		counts[t]++
	}
	var parts []string
	for t, c := range counts {
		if c > 1 {
			parts = append(parts, fmt.Sprintf("%s(%d)", t, c))
		} else {
			parts = append(parts, t)
		}
	}
	return fmt.Sprintf("%s", parts)
}

func makeLogger(verbose bool) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Printf("[galph] "+format+"\n", args...)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "galph: "+format+"\n", args...)
	os.Exit(1)
}
