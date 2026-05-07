package relay

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

type Usage struct {
	InputTokens         int64
	CachedInputTokens   int64
	OutputTokens        int64
	ReasoningTokens     int64
	TotalTokens         int64
	Model               string
	ServiceTier         string
	ResponseID          string
	Finished            bool
	IncompleteReason    string
}

type usageScanner struct {
	buf     bytes.Buffer
	current Usage
}

func newUsageScanner() *usageScanner {
	return &usageScanner{}
}

func (s *usageScanner) Feed(chunk []byte) {
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

func (s *usageScanner) Done() Usage {
	if s.buf.Len() > 0 {
		s.parseEvent(s.buf.Bytes())
		s.buf.Reset()
	}
	return s.current
}

func (s *usageScanner) parseEvent(event []byte) {
	if len(event) == 0 {
		return
	}
	var evType string
	var dataLines [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			evType = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			dataLines = append(dataLines, bytes.TrimPrefix(line, []byte("data: ")))
		}
	}
	if len(dataLines) == 0 {
		return
	}
	data := bytes.Join(dataLines, []byte("\n"))
	switch evType {
	case "response.completed", "response.incomplete":
		reason := gjson.GetBytes(data, "response.incomplete_details.reason").String()
		if reason == "" {
			reason = gjson.GetBytes(data, "incomplete_details.reason").String()
		}
		s.captureFromResponse(data, evType == "response.incomplete", reason)
	case "response.created":
		if id := gjson.GetBytes(data, "response.id").String(); id != "" {
			s.current.ResponseID = id
		}
		if m := gjson.GetBytes(data, "response.model").String(); m != "" {
			s.current.Model = m
		}
	}
}

func (s *usageScanner) captureFromResponse(data []byte, incomplete bool, reason string) {
	resp := gjson.GetBytes(data, "response")
	if !resp.Exists() {
		resp = gjson.ParseBytes(data)
	}
	usage := resp.Get("usage")
	if !usage.Exists() {
		return
	}
	s.current.InputTokens = usage.Get("input_tokens").Int()
	s.current.CachedInputTokens = usage.Get("input_tokens_details.cached_tokens").Int()
	s.current.OutputTokens = usage.Get("output_tokens").Int()
	s.current.ReasoningTokens = usage.Get("output_tokens_details.reasoning_tokens").Int()
	s.current.TotalTokens = usage.Get("total_tokens").Int()
	if s.current.TotalTokens == 0 {
		s.current.TotalTokens = s.current.InputTokens + s.current.OutputTokens
	}
	if id := resp.Get("id").String(); id != "" {
		s.current.ResponseID = id
	}
	if m := resp.Get("model").String(); m != "" {
		s.current.Model = m
	}
	if tier := resp.Get("service_tier").String(); tier != "" {
		s.current.ServiceTier = tier
	}
	s.current.Finished = true
	if incomplete {
		s.current.IncompleteReason = reason
	}
}
