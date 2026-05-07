package relay

import (
	"bytes"

	"github.com/tidwall/gjson"
)

type chatScanner struct {
	buf     bytes.Buffer
	current Usage
}

func newChatScanner() *chatScanner {
	return &chatScanner{}
}

func (s *chatScanner) Feed(chunk []byte) {
	s.buf.Write(chunk)
	for {
		idx := bytes.Index(s.buf.Bytes(), []byte("\n\n"))
		if idx < 0 {
			return
		}
		event := s.buf.Next(idx + 2)
		s.parseEvent(event[:len(event)-2])
	}
}

func (s *chatScanner) Done() Usage {
	if s.buf.Len() > 0 {
		s.parseEvent(s.buf.Bytes())
		s.buf.Reset()
	}
	return s.current
}

func (s *chatScanner) parseEvent(event []byte) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			s.current.Finished = true
			if s.current.TotalTokens == 0 {
				s.current.TotalTokens = s.current.InputTokens + s.current.OutputTokens
			}
			continue
		}
		if id := gjson.GetBytes(data, "id").String(); id != "" {
			s.current.ResponseID = id
		}
		if m := gjson.GetBytes(data, "model").String(); m != "" {
			s.current.Model = m
		}
		if tier := gjson.GetBytes(data, "service_tier").String(); tier != "" {
			s.current.ServiceTier = tier
		}
		usage := gjson.GetBytes(data, "usage")
		if usage.Exists() {
			if v := usage.Get("prompt_tokens").Int(); v > 0 {
				s.current.InputTokens = v
			}
			if v := usage.Get("completion_tokens").Int(); v > 0 {
				s.current.OutputTokens = v
			}
			if v := usage.Get("total_tokens").Int(); v > 0 {
				s.current.TotalTokens = v
			}
			if v := usage.Get("prompt_tokens_details.cached_tokens").Int(); v > 0 {
				s.current.CachedInputTokens = v
			}
			if v := usage.Get("completion_tokens_details.reasoning_tokens").Int(); v > 0 {
				s.current.ReasoningTokens = v
			}
		}
	}
}

type ChatUsageExtractor struct {
	scanner *chatScanner
}

func NewChatUsageExtractor() *ChatUsageExtractor {
	return &ChatUsageExtractor{scanner: newChatScanner()}
}

func (e *ChatUsageExtractor) Write(p []byte) (int, error) {
	e.scanner.Feed(p)
	return len(p), nil
}

func (e *ChatUsageExtractor) Done() Usage {
	return e.scanner.Done()
}
