// Package sse provides a reader for Server-Sent Events streams.
//
// SSE is the wire format used by OpenAI and Anthropic for streaming
// chat completions. This package handles the line protocol so that
// provider implementations only deal with parsed events.
//
// Reference: https://html.spec.whatwg.org/multipage/server-sent-events.html
package sse

import (
	"bufio"
	"io"
	"iter"
	"strings"
)

// Event is a single SSE event parsed from the stream.
type Event struct {
	// Type is the event type (from the "event:" field).
	// Empty for events that only have a data field (e.g., OpenAI).
	Type string
	// Data is the event payload (from the "data:" field).
	// Multiple data lines are joined with newlines.
	Data string
}

// Read returns an iterator over SSE events from the given reader.
// It handles the standard SSE line protocol: event/data fields separated
// by blank lines. The iterator yields events as they're parsed from the stream.
//
// Usage:
//
//	for event, err := range sse.Read(resp.Body) {
//	    if err != nil { ... }
//	    if event.Data == "[DONE]" { break }
//	    // process event
//	}
func Read(r io.Reader) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		scanner := bufio.NewScanner(r)
		// Set a reasonable max line size for SSE data lines (1 MB).
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var event Event
		for scanner.Scan() {
			line := scanner.Text()

			switch {
			case line == "":
				// Empty line = end of event.
				if event.Data != "" {
					if !yield(event, nil) {
						return
					}
					event = Event{}
				}

			case strings.HasPrefix(line, "event:"):
				event.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))

			case strings.HasPrefix(line, "data:"):
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if event.Data != "" {
					event.Data += "\n" + data
				} else {
					event.Data = data
				}

			default:
				// Comment (":"), id:, retry: lines — ignored.
			}
		}

		// Yield any remaining event (stream may end without trailing newline).
		if event.Data != "" {
			if !yield(event, nil) {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			yield(Event{}, err)
		}
	}
}
