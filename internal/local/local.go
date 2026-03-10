// Package local implements a host-native executor for galph.
// Commands run directly on the macOS/Linux host instead of inside a Docker container.
// This is required for projects needing platform-specific toolchains (e.g., Swift/Xcode).
package local

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// envVarsToForward lists environment variables passed to klaudia subprocesses.
var envVarsToForward = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"KLAUDIA_CUSTOM_MODEL",
	"KLAUDIA_CUSTOM_ENDPOINT",
	"PATH",
	"HOME",
	"USER",
	"SHELL",
	"TERM",
	"LANG",
}

// Executor runs commands directly on the host.
type Executor struct {
	workspace  string // absolute path to project directory
	klaudiaDir string // absolute path to klaudia installation
}

// New creates a local executor with resolved paths.
func New(workspace, klaudiaDir string) (*Executor, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace: %w", err)
	}
	absKlaudia, err := filepath.Abs(klaudiaDir)
	if err != nil {
		return nil, fmt.Errorf("resolving klaudia dir: %w", err)
	}

	// Verify klaudia exists
	cliPath := filepath.Join(absKlaudia, "dist", "cli.js")
	if _, err := os.Stat(cliPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("klaudia not found at %s", cliPath)
	}

	return &Executor{
		workspace:  absWorkspace,
		klaudiaDir: absKlaudia,
	}, nil
}

func (e *Executor) Start() error    { return nil }
func (e *Executor) Stop() error     { return nil }
func (e *Executor) Remove() error   { return nil }
func (e *Executor) IsRunning() bool { return true }

func (e *Executor) WorkspacePath() string { return e.workspace }

func (e *Executor) KlaudiaCmd() []string {
	return []string{"node", filepath.Join(e.klaudiaDir, "dist", "cli.js")}
}

// Exec runs a command in the workspace directory and returns stdout.
func (e *Executor) Exec(command []string, onOutput func(string)) (string, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = e.workspace
	cmd.Env = e.buildEnv()

	var stdout, stderr bytes.Buffer

	if onOutput != nil {
		pr, pw := io.Pipe()
		cmd.Stdout = io.MultiWriter(&stdout, pw)
		cmd.Stderr = &stderr

		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := pr.Read(buf)
				if n > 0 {
					lines := strings.Split(string(buf[:n]), "\n")
					for _, line := range lines {
						line = strings.TrimSpace(line)
						if line != "" {
							onOutput(line)
						}
					}
				}
				if err != nil {
					return
				}
			}
		}()

		err := cmd.Run()
		pw.Close()
		if err != nil {
			return stdout.String(), fmt.Errorf("exec: %w\n%s", err, stderr.String())
		}
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("exec: %w\n%s", err, stderr.String())
		}
	}

	return stdout.String(), nil
}

// ExecStream runs a command and pipes stdout to the given writer.
func (e *Executor) ExecStream(command []string, stdout io.Writer) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = e.workspace
	cmd.Env = e.buildEnv()
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec: %w\n%s", err, stderr.String())
	}
	return nil
}

// buildEnv constructs the environment for subprocesses.
func (e *Executor) buildEnv() []string {
	env := []string{
		"CLAUDECODE=",                          // prevent nested-session detection
		"CLAUDE_CODE_ENTRYPOINT=local-agent",   // tell klaudia it's agent-driven
	}

	for _, key := range envVarsToForward {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}

	return env
}
