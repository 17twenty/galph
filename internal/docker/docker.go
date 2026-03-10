// Package docker manages the Klaudia container lifecycle.
// Uses the docker CLI directly (no SDK dependency).
package docker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// envVarsToForward lists environment variables passed from host to container.
// Auth-related vars so klaudia can authenticate, plus overrides.
var envVarsToForward = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"KLAUDIA_CUSTOM_MODEL",
	"KLAUDIA_CUSTOM_ENDPOINT",
}

// ContainerName generates a unique, deterministic container name from the project
// name and workspace path. This prevents clashing when running multiple galph instances.
func ContainerName(projectName, workspacePath string) string {
	// Sanitize project name for Docker (alphanumeric, hyphens, underscores)
	safe := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(projectName, "")
	if safe == "" {
		safe = "galph"
	}

	// Short hash of workspace path for uniqueness
	h := sha256.Sum256([]byte(workspacePath))
	short := fmt.Sprintf("%x", h[:4])

	return fmt.Sprintf("galph-%s-%s", safe, short)
}

// Container holds the state of a running galph container.
type Container struct {
	Name       string
	Image      string
	Workspace  string // Absolute path to project workspace on host
	KlaudiaDir string // Absolute path to klaudia installation on host
	ClaudeDir  string // Absolute path to ~/.claude on host
	GalphDir   string // Absolute path to .galph state dir on host
	Memory     string // Memory limit (e.g., "4g")
	Network    string // Network mode (e.g., "host", "bridge")
}

// NewContainer creates a container config with resolved paths.
func NewContainer(name, image, workspace, klaudiaDir, claudeDir, galphDir, memory, network string) (*Container, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}

	absKlaudia, err := filepath.Abs(klaudiaDir)
	if err != nil {
		return nil, fmt.Errorf("resolving klaudia dir path: %w", err)
	}

	absClaude, err := filepath.Abs(claudeDir)
	if err != nil {
		return nil, fmt.Errorf("resolving claude dir path: %w", err)
	}

	absGalph, err := filepath.Abs(galphDir)
	if err != nil {
		return nil, fmt.Errorf("resolving galph dir path: %w", err)
	}

	return &Container{
		Name:       name,
		Image:      image,
		Workspace:  absWorkspace,
		KlaudiaDir: absKlaudia,
		ClaudeDir:  absClaude,
		GalphDir:   absGalph,
		Memory:     memory,
		Network:    network,
	}, nil
}

// WorkspacePath returns the workspace path as seen inside the container.
func (c *Container) WorkspacePath() string { return "/workspace" }

// KlaudiaCmd returns the command to invoke klaudia inside the container.
func (c *Container) KlaudiaCmd() []string {
	return []string{"node", "/klaudia/dist/cli.js"}
}

// Build builds the Docker image from a Dockerfile.
func Build(dockerfilePath, image string) error {
	dir := filepath.Dir(dockerfilePath)
	cmd := exec.Command("docker", "build", "-t", image, "-f", dockerfilePath, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	return nil
}

// ImageExists checks if a Docker image exists locally.
func ImageExists(image string) bool {
	cmd := exec.Command("docker", "image", "inspect", image)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// Start creates and starts the container.
func (c *Container) Start() error {
	// Remove any existing container with the same name
	_ = c.Remove()

	args := []string{
		"run", "-d",
		"--name", c.Name,
		// Volume mounts
		"-v", c.Workspace + ":/workspace",
		"-v", c.KlaudiaDir + ":/klaudia:ro",
		"-v", c.ClaudeDir + ":/home/node/.claude",
		"-v", c.GalphDir + ":/home/node/.galph",
		// Mount .claude.json from home dir (auth/config lives here, not in .claude/)
		"-v", filepath.Join(filepath.Dir(c.ClaudeDir), ".claude.json") + ":/home/node/.claude.json",
		// Working directory
		"-w", "/workspace",
		// Memory limit
		"--memory", c.Memory,
		// Unset CLAUDECODE to prevent nested-session detection
		"-e", "CLAUDECODE=",
		// Tell klaudia it's being driven by an agent
		"-e", "CLAUDE_CODE_ENTRYPOINT=local-agent",
	}

	// Forward auth and provider env vars from host
	for _, key := range envVarsToForward {
		if val, ok := os.LookupEnv(key); ok && val != "" {
			args = append(args, "-e", key+"="+val)
		}
	}

	// If no API key is set, try to extract OAuth token from macOS keychain
	// (keychain isn't available inside the Linux container)
	if oauthToken := resolveOAuthToken(); oauthToken != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	if c.Network != "" {
		args = append(args, "--network", c.Network)
	}

	// Keep container alive
	args = append(args, "--entrypoint", "sleep")
	args = append(args, c.Image, "infinity")

	cmd := exec.Command("docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run: %w\n%s", err, stderr.String())
	}
	return nil
}

// Exec runs a command in the container and returns stdout.
// The onOutput callback streams stdout lines as they arrive.
func (c *Container) Exec(command []string, onOutput func(line string)) (string, error) {
	args := append([]string{"exec", c.Name}, command...)
	cmd := exec.Command("docker", args...)

	var stdout, stderr bytes.Buffer

	if onOutput != nil {
		// Stream stdout line by line
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
			return stdout.String(), fmt.Errorf("docker exec: %w\n%s", err, stderr.String())
		}
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return stdout.String(), fmt.Errorf("docker exec: %w\n%s", err, stderr.String())
		}
	}

	return stdout.String(), nil
}

// ExecStream runs a command and pipes stdout to the given writer.
// Stderr is captured and included in the error if the command fails.
func (c *Container) ExecStream(command []string, stdout io.Writer) error {
	args := append([]string{"exec", c.Name}, command...)
	cmd := exec.Command("docker", args...)
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker exec: %w\n%s", err, stderr.String())
	}
	return nil
}

// IsRunning checks if the container is running.
func (c *Container) IsRunning() bool {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", c.Name)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Stop stops the container.
func (c *Container) Stop() error {
	cmd := exec.Command("docker", "stop", "-t", "5", c.Name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// Remove removes the container.
func (c *Container) Remove() error {
	cmd := exec.Command("docker", "rm", "-f", c.Name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// resolveOAuthToken extracts the Claude OAuth token from the macOS keychain.
// Returns empty string on non-macOS or if no token is found.
func resolveOAuthToken() string {
	// Check env var first
	if token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); token != "" {
		return token
	}

	// Try macOS keychain
	cmd := exec.Command("security", "find-generic-password", "-a", os.Getenv("USER"), "-w", "-s", "Claude Code-credentials")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse the JSON to extract accessToken
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(out, &creds); err != nil {
		return ""
	}
	return creds.ClaudeAiOauth.AccessToken
}
