package relay

import (
	"strings"
	"testing"
)

const completedEvent = `event: response.created
data: {"type":"response.created","response":{"id":"resp_abc","model":"gpt-5","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_abc","model":"gpt-5","status":"completed","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":80},"output_tokens":42,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":162}}}

`

func TestUsageScannerExtractsCompletedEvent(t *testing.T) {
	s := newUsageScanner()
	s.Feed([]byte(completedEvent))
	u := s.Done()
	if u.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want 120", u.InputTokens)
	}
	if u.CachedInputTokens != 80 {
		t.Errorf("CachedInputTokens = %d, want 80", u.CachedInputTokens)
	}
	if u.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", u.OutputTokens)
	}
	if u.ReasoningTokens != 10 {
		t.Errorf("ReasoningTokens = %d, want 10", u.ReasoningTokens)
	}
	if u.TotalTokens != 162 {
		t.Errorf("TotalTokens = %d, want 162", u.TotalTokens)
	}
	if u.Model != "gpt-5" {
		t.Errorf("Model = %q", u.Model)
	}
	if u.ResponseID != "resp_abc" {
		t.Errorf("ResponseID = %q", u.ResponseID)
	}
	if !u.Finished {
		t.Errorf("Finished should be true")
	}
}

func TestUsageScannerHandlesByteSplit(t *testing.T) {
	s := newUsageScanner()
	chunks := []string{}
	for i := 0; i < len(completedEvent); i += 17 {
		end := i + 17
		if end > len(completedEvent) {
			end = len(completedEvent)
		}
		chunks = append(chunks, completedEvent[i:end])
	}
	for _, c := range chunks {
		s.Feed([]byte(c))
	}
	u := s.Done()
	if u.InputTokens != 120 || u.OutputTokens != 42 {
		t.Errorf("byte-split feed lost data: input=%d output=%d", u.InputTokens, u.OutputTokens)
	}
}

func TestUsageScannerIncomplete(t *testing.T) {
	stream := strings.Replace(completedEvent, "response.completed", "response.incomplete", 1)
	stream = strings.Replace(stream, `"status":"completed"`, `"status":"incomplete"`, 1)
	stream = strings.Replace(stream, `"usage"`, `"incomplete_details":{"reason":"max_output_tokens"},"usage"`, 1)
	s := newUsageScanner()
	s.Feed([]byte(stream))
	u := s.Done()
	if !u.Finished {
		t.Errorf("Finished should be true on incomplete event")
	}
	if u.IncompleteReason != "max_output_tokens" {
		t.Errorf("IncompleteReason = %q", u.IncompleteReason)
	}
}

func TestUsageScannerMissingUsage(t *testing.T) {
	s := newUsageScanner()
	s.Feed([]byte("event: response.completed\ndata: {\"response\":{\"id\":\"r1\"}}\n\n"))
	u := s.Done()
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("expected zero usage, got %+v", u)
	}
	if u.Finished {
		t.Errorf("Finished should be false when usage absent")
	}
}
