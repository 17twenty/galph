// Package parser handles Claude Code's --output-format stream-json output.
// Each line is a JSON object with a "type" field indicating the event kind.
package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EventType identifies the kind of stream event.
type EventType string

const (
	EventAssistant  EventType = "assistant"
	EventSystem     EventType = "system"
	EventToolUse    EventType = "tool_use"
	EventToolResult EventType = "tool_result"
	EventResult     EventType = "result"
)

// StreamEvent is the raw JSON envelope from stream-json output.
type StreamEvent struct {
	Type    EventType       `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`

	// tool_use fields
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result fields
	Content json.RawMessage `json:"content,omitempty"`

	// result fields
	DurationMS   int     `json:"duration_ms,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`

	// Session info
	SessionID string `json:"session_id,omitempty"`
}

// ContentBlock represents a content item in an assistant message.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AssistantMessage is the parsed message field of an assistant event.
type AssistantMessage struct {
	Content []ContentBlock `json:"content"`
}

// IterationResult summarizes a single klaudia invocation.
type IterationResult struct {
	TextOutput  string   // Concatenated text from assistant messages
	ToolCalls   []string // Tool names used
	DurationMS  int
	CostUSD     float64
	IsError     bool
	NumTurns    int
	SessionID   string
	EventCount  int
	RawEvents   []StreamEvent
}

// ParseStream reads stream-json from r and returns a summary.
// The callback is called for each event as it arrives (for live display).
func ParseStream(r io.Reader, onEvent func(StreamEvent)) (*IterationResult, error) {
	result := &IterationResult{}
	scanner := bufio.NewScanner(r)

	// Increase buffer for large tool results
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var textParts []string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Skip non-JSON lines (e.g., stderr leaking in)
			continue
		}

		result.EventCount++
		result.RawEvents = append(result.RawEvents, event)

		if onEvent != nil {
			onEvent(event)
		}

		switch event.Type {
		case EventAssistant:
			var msg AssistantMessage
			if err := json.Unmarshal(event.Message, &msg); err == nil {
				for _, block := range msg.Content {
					if block.Type == "text" && block.Text != "" {
						textParts = append(textParts, block.Text)
					}
				}
			}

		case EventToolUse:
			if event.Name != "" {
				result.ToolCalls = append(result.ToolCalls, event.Name)
			}

		case EventResult:
			result.DurationMS = event.DurationMS
			// total_cost_usd is the actual field name; cost_usd is a fallback
			if event.TotalCostUSD > 0 {
				result.CostUSD = event.TotalCostUSD
			} else {
				result.CostUSD = event.CostUSD
			}
			result.IsError = event.IsError
			result.NumTurns = event.NumTurns
			result.SessionID = event.SessionID
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("reading stream: %w", err)
	}

	result.TextOutput = strings.Join(textParts, "\n")
	return result, nil
}

// ParseStreamJSON parses a complete stream-json string (e.g., from a file).
func ParseStreamJSON(data string) (*IterationResult, error) {
	return ParseStream(strings.NewReader(data), nil)
}
