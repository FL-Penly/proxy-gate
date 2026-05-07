package ingress

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxRequestBody = 64 << 20

func ReadAndDecompress(r *http.Request) ([]byte, string, error) {
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return nil, enc, fmt.Errorf("ingress: read body: %w", err)
	}
	if enc == "" || enc == "identity" {
		return raw, "", nil
	}
	plain, err := decompress(raw, enc)
	if err != nil {
		return nil, enc, err
	}
	return plain, enc, nil
}

func decompress(buf []byte, enc string) ([]byte, error) {
	switch enc {
	case "zstd":
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		return dec.DecodeAll(buf, nil)
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)
	case "deflate":
		fl := flate.NewReader(bytes.NewReader(buf))
		defer fl.Close()
		return io.ReadAll(fl)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(buf)))
	default:
		return nil, fmt.Errorf("ingress: unsupported encoding %q", enc)
	}
}

func AdaptForChatGPTBackend(body []byte) ([]byte, error) {
	return adaptForChatGPTBackend(body, false)
}

func AdaptForChatGPTCompact(body []byte) ([]byte, error) {
	return adaptForChatGPTBackend(body, true)
}

func adaptForChatGPTBackend(body []byte, compact bool) ([]byte, error) {
	if !gjson.GetBytes(body, "instructions").Exists() {
		var err error
		body, err = sjson.SetBytes(body, "instructions", "")
		if err != nil {
			return nil, err
		}
	}
	body, err := sjson.SetBytes(body, "store", false)
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetBytes(body, "stream", !compact)
	if err != nil {
		return nil, err
	}
	body, err = sjson.DeleteBytes(body, "max_output_tokens")
	if err != nil {
		return nil, err
	}
	body, err = sjson.DeleteBytes(body, "max_tokens")
	if err != nil {
		return nil, err
	}
	return body, nil
}

func IsStreaming(body []byte) bool {
	streamField := gjson.GetBytes(body, "stream")
	if !streamField.Exists() {
		return true
	}
	return streamField.Bool()
}

func ExtractModel(body []byte) string {
	m := gjson.GetBytes(body, "model").String()
	if m == "" {
		return "unknown"
	}
	return m
}

func ExtractServiceTier(body []byte) string {
	return gjson.GetBytes(body, "service_tier").String()
}

func ExtractPreviousResponseID(body []byte) string {
	return gjson.GetBytes(body, "previous_response_id").String()
}

var ErrAdminTokenMissing = errors.New("admin: token not configured")

func RequireAdmin(token string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "admin token not configured", http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Admin-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func RequireProxyToken(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.Header.Get("X-ProxyGate-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
