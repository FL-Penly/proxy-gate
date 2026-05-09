package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/browser"
)

const (
	OpenAIClientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAIAuthURL          = "https://auth.openai.com/oauth/authorize"
	OpenAITokenURL         = "https://auth.openai.com/oauth/token"
	OpenAICallbackPath     = "/auth/callback"
	ClaudeClientID         = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	ClaudeAuthURL          = "https://claude.ai/oauth/authorize"
	ClaudeCallbackPath     = "/callback"
	defaultCallbackTimeout = 2 * time.Minute
)

var OpenAICallbackPorts = []int{1455, 1456, 1457, 1458, 1459, 1460}
var ClaudeCallbackPorts = []int{1461, 1462, 1463, 1464, 1465, 1466}

var OpenAIScopes = []string{"openid", "profile", "email", "offline_access"}
var ClaudeScopes = []string{"org:create_api_key", "user:profile", "user:inference", "user:sessions:claude_code", "user:file_upload"}

type PKCE struct {
	Verifier  string
	Challenge string
}

func NewPKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

func NewState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type AuthorizeRequest struct {
	ClientID    string
	RedirectURI string
	Challenge   string
	State       string
	Scopes      []string
	Extra       url.Values
}

func (r AuthorizeRequest) URL() string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", r.ClientID)
	v.Set("redirect_uri", r.RedirectURI)
	v.Set("scope", joinSpaces(r.Scopes))
	v.Set("code_challenge", r.Challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", r.State)
	for k, vs := range r.Extra {
		for _, val := range vs {
			v.Add(k, val)
		}
	}
	return OpenAIAuthURL + "?" + v.Encode()
}

func OpenAIAuthorizeURL(redirectURI, challenge, state string) string {
	return AuthorizeRequest{
		ClientID:    OpenAIClientID,
		RedirectURI: redirectURI,
		Challenge:   challenge,
		State:       state,
		Scopes:      OpenAIScopes,
		Extra: url.Values{
			"id_token_add_organizations": []string{"true"},
			"codex_cli_simplified_flow":  []string{"true"},
			"originator":                 []string{"codex_cli_rs"},
			"prompt":                     []string{"login"},
			"max_age":                    []string{"0"},
		},
	}.URL()
}

func ClaudeAuthorizeURL(redirectURI, challenge, state string) string {
	v := url.Values{}
	v.Set("code", "true")
	v.Set("response_type", "code")
	v.Set("client_id", ClaudeClientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", joinSpaces(ClaudeScopes))
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", state)
	return ClaudeAuthURL + "?" + v.Encode()
}

type CallbackResult struct {
	Code  string
	State string
}

type Callback struct {
	server *http.Server
	port   int

	mu     sync.Mutex
	result *CallbackResult
	err    error
	done   chan struct{}

	expectedState string
	callbackPath  string
}

func StartOpenAICallback(ctx context.Context, expectedState string) (*Callback, error) {
	return startCallback(ctx, expectedState, OpenAICallbackPath, OpenAICallbackPorts)
}

func StartClaudeCallback(ctx context.Context, expectedState string) (*Callback, error) {
	return startCallback(ctx, expectedState, ClaudeCallbackPath, ClaudeCallbackPorts)
}

func startCallback(ctx context.Context, expectedState, path string, ports []int) (*Callback, error) {
	cb := &Callback{
		expectedState: expectedState,
		callbackPath:  path,
		done:          make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, cb.handle)
	mux.HandleFunc("/success", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(successHTML("Login successful")))
	})

	var listener net.Listener
	var bindErr error
	for _, p := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			listener = l
			cb.port = p
			break
		}
		bindErr = err
	}
	if listener == nil {
		return nil, fmt.Errorf("bind callback ports %v: %w", ports, bindErr)
	}

	cb.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := cb.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cb.mu.Lock()
			if cb.err == nil {
				cb.err = err
			}
			cb.mu.Unlock()
			cb.signalDone()
		}
	}()
	return cb, nil
}

func (c *Callback) Port() int { return c.port }

func (c *Callback) RedirectURI() string {
	return fmt.Sprintf("http://localhost:%d%s", c.port, c.callbackPath)
}

func (c *Callback) Wait(ctx context.Context) (CallbackResult, error) {
	deadline, _ := ctx.Deadline()
	timeout := defaultCallbackTimeout
	if !deadline.IsZero() {
		timeout = time.Until(deadline)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil {
			return CallbackResult{}, c.err
		}
		if c.result == nil {
			return CallbackResult{}, errors.New("oauth: no result")
		}
		return *c.result, nil
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	case <-timer.C:
		return CallbackResult{}, errors.New("oauth: callback timeout")
	}
}

func (c *Callback) Close() {
	if c.server != nil {
		_ = c.server.Close()
	}
}

func (c *Callback) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errStr := q.Get("error"); errStr != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorHTML(errStr)))
		c.fail(fmt.Errorf("oauth error: %s", errStr))
		return
	}
	state := q.Get("state")
	code := q.Get("code")
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("waiting for authorization code"))
		return
	}
	if c.expectedState != "" && state != c.expectedState {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errorHTML("state mismatch")))
		c.fail(errors.New("oauth: state mismatch"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(successHTML("Authentication successful — you can close this window.")))
	c.succeed(CallbackResult{Code: code, State: state})
}

func (c *Callback) succeed(res CallbackResult) {
	c.mu.Lock()
	if c.result == nil && c.err == nil {
		r := res
		c.result = &r
	}
	c.mu.Unlock()
	c.signalDone()
}

func (c *Callback) fail(err error) {
	c.mu.Lock()
	if c.err == nil && c.result == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.signalDone()
}

func (c *Callback) signalDone() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func OpenBrowser(rawurl string) error {
	return browser.OpenURL(rawurl)
}

func successHTML(msg string) string {
	return `<!doctype html><meta charset="utf-8"><title>Done</title><body style="font-family:system-ui;background:#0f172a;color:#f8fafc;display:flex;align-items:center;justify-content:center;height:100vh;margin:0"><div style="background:#1e293b;padding:3rem;border-radius:1rem;text-align:center;max-width:420px"><h1 style="color:#10b981;margin:0 0 1rem">Success</h1><p style="color:#94a3b8">` + htmlEscape(msg) + `</p></div></body>`
}

func errorHTML(reason string) string {
	return `<!doctype html><meta charset="utf-8"><title>Failed</title><body style="font-family:system-ui;background:#0f172a;color:#f8fafc;display:flex;align-items:center;justify-content:center;height:100vh;margin:0"><div style="background:#1e293b;padding:3rem;border-radius:1rem;text-align:center;max-width:420px"><h1 style="color:#ef4444;margin:0 0 1rem">Failed</h1><pre style="color:#fca5a5;background:rgba(239,68,68,0.1);padding:1rem;border-radius:0.5rem">` + htmlEscape(reason) + `</pre></div></body>`
}

func htmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '<':
			out = append(out, '&', 'l', 't', ';')
		case '>':
			out = append(out, '&', 'g', 't', ';')
		case '&':
			out = append(out, '&', 'a', 'm', 'p', ';')
		case '"':
			out = append(out, '&', 'q', 'u', 'o', 't', ';')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}

func joinSpaces(xs []string) string {
	out := ""
	for i, s := range xs {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
