// Package config handles galph configuration from .galphrc files and CLI flags.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all galph settings.
type Config struct {
	ProjectName            string       `json:"project_name,omitempty"`
	Workspace              string       `json:"workspace"`
	PRD                    string       `json:"prd"`
	MaxIterations          int          `json:"max_iterations"`
	MaxConsecutiveFailures int          `json:"max_consecutive_failures"`
	TestCommand            string       `json:"test_command"`
	Model                  string       `json:"model"`
	Mode                   string       `json:"mode"` // "docker" (default) or "local"
	Docker                 DockerConfig `json:"docker"`
	DryRun                 bool         `json:"dry_run"`
	Verbose                bool         `json:"verbose"`
}

// DockerConfig holds container settings.
type DockerConfig struct {
	Image      string `json:"image"`
	Memory     string `json:"memory"`
	Network    string `json:"network"`
	KlaudiaDir string `json:"klaudia_dir,omitempty"` // Path to klaudia on host
}

// DefaultConfig returns sensible defaults for a local-first project.
func DefaultConfig() *Config {
	return &Config{
		Workspace:              ".",
		PRD:                    "PRD.md",
		MaxIterations:          50,
		MaxConsecutiveFailures: 3,
		TestCommand:            "",
		Model:                  "claude-sonnet-4-6",
		Mode:                   "docker",
		Docker: DockerConfig{
			Image:   "galph-klaudia",
			Memory:  "4g",
			Network: "host",
		},
	}
}

// Load reads config from .galphrc in the given directory (JSON format).
// Missing file is not an error — defaults are used.
func Load(dir string) (*Config, error) {
	cfg := DefaultConfig()

	rcPath := filepath.Join(dir, ".galphrc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading %s: %w", rcPath, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", rcPath, err)
	}

	return cfg, nil
}

// IsGalphProject checks if the given directory looks like a galph project
// (has .galphrc or PRD.md).
func IsGalphProject(dir string) bool {
	for _, f := range []string{".galphrc", "PRD.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

// WriteRC writes a .galphrc file to the given directory.
func WriteRC(dir string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, ".galphrc"), data, 0o644)
}

// ResolveWorkspace resolves the workspace path relative to the given base dir.
func (c *Config) ResolveWorkspace(baseDir string) string {
	if filepath.IsAbs(c.Workspace) {
		return c.Workspace
	}
	return filepath.Join(baseDir, c.Workspace)
}

// ResolvePRD resolves the PRD path relative to the workspace.
func (c *Config) ResolvePRD() string {
	if filepath.IsAbs(c.PRD) {
		return c.PRD
	}
	return filepath.Join(c.Workspace, c.PRD)
}

// ResolveKlaudiaDir finds the klaudia installation directory.
// Priority: explicit config > sibling of galph binary > sibling of CWD.
func (c *Config) ResolveKlaudiaDir() (string, error) {
	// 1. Explicit config
	if c.Docker.KlaudiaDir != "" {
		abs, err := filepath.Abs(c.Docker.KlaudiaDir)
		if err != nil {
			return "", err
		}
		if isKlaudiaDir(abs) {
			return abs, nil
		}
		return "", fmt.Errorf("klaudia_dir %q doesn't contain dist/cli.js", abs)
	}

	// 2. Sibling of the galph binary (../klaudia relative to binary)
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "klaudia")
		if abs, err := filepath.Abs(candidate); err == nil && isKlaudiaDir(abs) {
			return abs, nil
		}
	}

	// 3. Sibling of CWD (../klaudia relative to working dir)
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "..", "klaudia")
		if abs, err := filepath.Abs(candidate); err == nil && isKlaudiaDir(abs) {
			return abs, nil
		}
	}

	return "", fmt.Errorf("cannot find klaudia installation (set klaudia_dir in .galphrc or place klaudia/ as sibling)")
}

func isKlaudiaDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "dist", "cli.js"))
	return err == nil
}
