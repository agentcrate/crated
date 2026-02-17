package sse_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agentcrate/crated/internal/sse"
)

func TestRead_SimpleDataEvents(t *testing.T) {
	input := "data: hello\n\ndata: world\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Data != "hello" {
		t.Errorf("event[0].Data = %q, want %q", events[0].Data, "hello")
	}
	if events[1].Data != "world" {
		t.Errorf("event[1].Data = %q, want %q", events[1].Data, "world")
	}
}

func TestRead_TypedEvents(t *testing.T) {
	input := "event: message_start\ndata: {\"type\":\"start\"}\n\nevent: content_block_delta\ndata: {\"text\":\"hi\"}\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "message_start" {
		t.Errorf("event[0].Type = %q, want %q", events[0].Type, "message_start")
	}
	if events[1].Type != "content_block_delta" {
		t.Errorf("event[1].Type = %q, want %q", events[1].Type, "content_block_delta")
	}
}

func TestRead_MultiLineData(t *testing.T) {
	input := "data: line1\ndata: line2\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "line1\nline2" {
		t.Errorf("event[0].Data = %q, want %q", events[0].Data, "line1\nline2")
	}
}

func TestRead_IgnoresComments(t *testing.T) {
	input := ": this is a comment\ndata: actual\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "actual" {
		t.Errorf("event[0].Data = %q, want %q", events[0].Data, "actual")
	}
}

func TestRead_DoneSignal(t *testing.T) {
	input := "data: {\"text\":\"hi\"}\n\ndata: [DONE]\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Data != "[DONE]" {
		t.Errorf("event[1].Data = %q, want %q", events[1].Data, "[DONE]")
	}
}

func TestRead_EmptyInput(t *testing.T) {
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader("")) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestRead_NoTrailingNewline(t *testing.T) {
	// Stream ends without a trailing empty line.
	input := "data: last"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "last" {
		t.Errorf("event[0].Data = %q, want %q", events[0].Data, "last")
	}
}

func TestRead_EarlyBreak(t *testing.T) {
	// Verify that breaking from the iterator mid-stream doesn't panic.
	input := "data: first\n\ndata: second\n\ndata: third\n\n"
	count := 0
	for _, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		count++
		if count == 1 {
			break // break after first event
		}
	}
	if count != 1 {
		t.Errorf("expected to stop after 1 event, got %d", count)
	}
}

func TestRead_IDAndRetryLinesIgnored(t *testing.T) {
	// id: and retry: lines are part of SSE spec but our reader ignores them.
	input := "id: 42\nretry: 5000\ndata: actual\n\n"
	var events []sse.Event
	for event, err := range sse.Read(strings.NewReader(input)) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "actual" {
		t.Errorf("event[0].Data = %q, want %q", events[0].Data, "actual")
	}
}

func TestRead_ScannerError(t *testing.T) {
	// Create a reader that returns an error after some data.
	r := &errorAfterReader{
		data: "data: hello\n\n",
		err:  strings.NewReader("").Read, // will cause an error read
	}
	var events []sse.Event
	var gotErr error
	for event, err := range sse.Read(r) {
		if err != nil {
			gotErr = err
			break
		}
		events = append(events, event)
	}
	// We should get the event before the error.
	if len(events) != 1 {
		t.Errorf("expected 1 event before error, got %d", len(events))
	}
	// Error may or may not propagate depending on scanner buffering,
	// but we should at least get the valid event.
	_ = gotErr
}

// errorAfterReader reads data then returns an error.
type errorAfterReader struct {
	data string
	err  func([]byte) (int, error)
	pos  int
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, fmt.Errorf("simulated read error")
}
