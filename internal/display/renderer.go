package display

// Renderer is the abstraction that any display backend must implement.
//
// The runner calls these methods at lifecycle boundaries. Each method
// receives the full DisplayState so the renderer can repaint as needed.
// The ANSI renderer prints incrementally; a future TUI renderer would
// re-render the full screen from the state snapshot.
type Renderer interface {
	// Init sets up the terminal (alternate screen, raw mode, etc.).
	Init(state *DisplayState)

	// RenderBanner shows startup info (version, model, workspace, mode).
	RenderBanner(state *DisplayState)

	// RenderPlanResult shows the parsed task checklist after planning.
	RenderPlanResult(state *DisplayState)

	// RenderTaskStart is called when a task begins execution.
	RenderTaskStart(state *DisplayState)

	// RenderProgress is called periodically (~2s) during task execution.
	RenderProgress(state *DisplayState)

	// RenderPhaseChange is called when the phase transitions.
	RenderPhaseChange(state *DisplayState)

	// RenderIterationResult is called after an iteration completes.
	RenderIterationResult(state *DisplayState)

	// RenderLog renders a structured log entry.
	RenderLog(state *DisplayState, entry LogEntry)

	// RenderCompletion shows the final summary (success or failure).
	RenderCompletion(state *DisplayState)

	// Close tears down the renderer (restore terminal, etc.).
	Close()
}
