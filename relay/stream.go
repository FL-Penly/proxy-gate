package relay

import (
	"errors"
	"io"
	"net/http"
)

var passthroughResponseHeaders = []string{
	"openai-model",
	"x-openai-model",
	"x-models-etag",
	"x-reasoning-included",
	"x-codex-turn-state",
	"retry-after",
	"x-ratelimit-reset",
	"x-ratelimit-limit-requests",
	"x-ratelimit-remaining-requests",
	"x-ratelimit-limit-tokens",
	"x-ratelimit-remaining-tokens",
}

func CopyAllowedResponseHeaders(src, dst http.Header) {
	for _, name := range passthroughResponseHeaders {
		if v := src.Get(name); v != "" {
			dst.Set(name, v)
		}
	}
}

type StreamResult struct {
	Usage     Usage
	BytesOut  int64
	WriteErr  error
	UpstrErr  error
}

func RelaySSE(dst io.Writer, src io.Reader) StreamResult {
	scanner := newUsageScanner()
	buf := make([]byte, 16<<10)
	var written int64
	var writeErr, upstrErr error
	flusher, _ := dst.(http.Flusher)
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
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
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

func NewUsageExtractor() *UsageExtractor {
	return &UsageExtractor{scanner: newUsageScanner()}
}

type UsageExtractor struct {
	scanner *usageScanner
}

func (u *UsageExtractor) Write(p []byte) (int, error) {
	u.scanner.Feed(p)
	return len(p), nil
}

func (u *UsageExtractor) Done() Usage {
	return u.scanner.Done()
}
