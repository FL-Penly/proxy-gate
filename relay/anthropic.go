package relay

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

type anthropicScanner struct {
	buf     bytes.Buffer
	current Usage
}

func newAnthropicScanner() *anthropicScanner {
	return &anthropicScanner{}
}

func (s *anthropicScanner) Feed(chunk []byte) {
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

func (s *anthropicScanner) Done() Usage {
	if s.buf.Len() > 0 {
		s.parseEvent(s.buf.Bytes())
		s.buf.Reset()
	}
	return s.current
}

func (s *anthropicScanner) parseEvent(event []byte) {
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
	case "message_start":
		msg := gjson.GetBytes(data, "message")
		if id := msg.Get("id").String(); id != "" {
			s.current.ResponseID = id
		}
		if m := msg.Get("model").String(); m != "" {
			s.current.Model = m
		}
		usage := msg.Get("usage")
		if v := usage.Get("input_tokens").Int(); v > 0 {
			s.current.InputTokens = v
		}
		if v := usage.Get("cache_read_input_tokens").Int(); v > 0 {
			s.current.CachedInputTokens = v
		}
	case "message_delta":
		usage := gjson.GetBytes(data, "usage")
		if v := usage.Get("output_tokens").Int(); v > 0 {
			s.current.OutputTokens = v
		}
		if v := usage.Get("input_tokens").Int(); v > 0 {
			s.current.InputTokens = v
		}
	case "message_stop":
		s.current.Finished = true
		if s.current.TotalTokens == 0 {
			s.current.TotalTokens = s.current.InputTokens + s.current.OutputTokens
		}
	}
}

type AnthropicUsageExtractor struct {
	scanner *anthropicScanner
}

func NewAnthropicUsageExtractor() *AnthropicUsageExtractor {
	return &AnthropicUsageExtractor{scanner: newAnthropicScanner()}
}

func (e *AnthropicUsageExtractor) Write(p []byte) (int, error) {
	e.scanner.Feed(p)
	return len(p), nil
}

func (e *AnthropicUsageExtractor) Done() Usage {
	return e.scanner.Done()
}

func RelayAnthropicSSE(dst writeFlusher, src reader) StreamResult {
	scanner := newAnthropicScanner()
	buf := make([]byte, 16<<10)
	var written int64
	var writeErr, upstrErr error
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			scanner.Feed(chunk)
			w, werr := dst.Write(chunk)
			written += int64(w)
			if werr != nil {
				writeErr = werr
				break
			}
			dst.Flush()
		}
		if rerr != nil {
			if rerr.Error() != "EOF" {
				upstrErr = rerr
			}
			break
		}
	}
	return StreamResult{
		Usage:    scanner.Done(),
		BytesOut: written,
		WriteErr: writeErr,
		UpstrErr: upstrErr,
	}
}

type writeFlusher interface {
	Write(p []byte) (int, error)
	Flush()
}

type reader interface {
	Read(p []byte) (int, error)
}
