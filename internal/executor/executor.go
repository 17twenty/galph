// Package executor defines the interface for running commands in galph.
// Implementations exist for Docker (container) and local (host) execution.
package executor

import "io"

// Executor runs commands either in a Docker container or directly on the host.
type Executor interface {
	// Start prepares the executor (e.g., starts a container). No-op for local.
	Start() error
	// Stop tears down the executor (e.g., stops a container). No-op for local.
	Stop() error
	// Remove cleans up resources (e.g., removes a container). No-op for local.
	Remove() error
	// IsRunning returns true if the executor is ready to accept commands.
	IsRunning() bool

	// Exec runs a command and returns stdout. The onOutput callback streams
	// stdout lines as they arrive (may be nil).
	Exec(command []string, onOutput func(string)) (string, error)
	// ExecStream runs a command and pipes stdout to the given writer.
	ExecStream(command []string, stdout io.Writer) error

	// WorkspacePath returns the workspace path as seen by commands.
	// "/workspace" for Docker, actual host path for local.
	WorkspacePath() string
	// KlaudiaCmd returns the base command to invoke klaudia.
	// ["node", "/klaudia/dist/cli.js"] for Docker,
	// ["node", "<hostPath>/dist/cli.js"] for local.
	KlaudiaCmd() []string
}
