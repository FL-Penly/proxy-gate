package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"path/filepath"

	"github.com/FL-Penly/proxy-gate/auth"
	"github.com/FL-Penly/proxy-gate/broker"
	"github.com/FL-Penly/proxy-gate/cmd"
	"github.com/FL-Penly/proxy-gate/control"
	"github.com/FL-Penly/proxy-gate/ingress"
	"github.com/FL-Penly/proxy-gate/pricing"
	"github.com/FL-Penly/proxy-gate/provider"
	"github.com/FL-Penly/proxy-gate/store"
	"github.com/FL-Penly/proxy-gate/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}

	switch command {
	case "serve":
		return cmdServe(args)
	case "add-account":
		return cmdAddAccount(args)
	case "add-claude-account":
		return cmdAddClaudeAccount(args)
	case "list":
		return cmdList(args)
	case "list-claude":
		return cmdListClaude(args)
	case "status":
		return cmdStatus(args)
	case "disable":
		return cmdDisable(args, true)
	case "enable":
		return cmdDisable(args, false)
	case "import":
		return cmdImport(args)
	case "import-keys":
		return cmdImportKeys(args)
	case "version":
		fmt.Println("proxy-gate (dev)")
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `proxy-gate — AI API proxy

Commands:
  serve [--config=PATH]                 Run the HTTP proxy
  add-account [--config=PATH] [--no-browser]   Add a ChatGPT account via OAuth
  add-account --code=URL_OR_CODE        Add an account via a copy-pasted code
  add-claude-account --from=PATH [--email=EMAIL] Import a Claude Code OAuth account
  add-claude-account --no-browser               Print Claude auth URL for manual code paste
  add-claude-account --code=URL_OR_CODE          Add Claude account from pasted redirect URL
  list [--config=PATH]                  List accounts in pool/chatgpt/
  list-claude [--config=PATH]            List accounts in pool/claude/
  status [--config=PATH]                Show account/key counts and current usage
  disable <email|id> [--config=PATH]    Mark an account or API key disabled
  enable <email|id> [--config=PATH]     Re-enable an account or API key
  import --from=PATH [--config=PATH]    Import accounts.json from v1
  import-keys --from=PATH [--config=PATH]   Import api-keys.json from v1
  version                                Print version
  help                                   Show this message`)
}

func parseConfigPath(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return "./config.toml"
}

func cmdServe(args []string) error {
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	logger := newLogger(cfg.Log.Level)
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.Paths.DataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir data: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	queue := control.NewQueue(st)
	defer queue.Close()

	pool := broker.NewPool(broker.PoolConfig{
		PrimaryUsedPctMax:   cfg.Broker.PrimaryUsedPctMax,
		SecondaryUsedPctMax: cfg.Broker.SecondaryUsedPctMax,
		Weights: broker.ScoreWeights{
			DrainMultiplier: cfg.Routing.DrainMultiplier,
			PrimaryBonus:    cfg.Routing.PrimaryBonus,
			InflightPenalty: 0.5,
		},
	})
	pool.SetPinStore(control.NewPinStore(st, queue), cfg.Broker.PinTTL)
	chatgptDir := cfg.PoolSubdir("chatgpt")
	if err := pool.LoadDir(chatgptDir); err != nil {
		return err
	}
	logger.Info("pool loaded", "accounts", pool.Len(), "dir", chatgptDir)

	keyPool := broker.NewAPIKeyPool()
	keyDir := cfg.PoolSubdir("apikeys")
	if err := keyPool.LoadDir(keyDir); err != nil {
		return err
	}
	logger.Info("key pool loaded", "keys", keyPool.Len(), "dir", keyDir)

	claudePool := broker.NewClaudePool()
	claudeDir := cfg.PoolSubdir("claude")
	if err := claudePool.LoadDir(claudeDir); err != nil {
		return err
	}
	logger.Info("claude pool loaded", "accounts", claudePool.Len(), "dir", claudeDir)

	loadAccountStats(st, pool)
	loadClaudeStats(st, claudePool)
	loadKeyStats(st, keyPool)
	claudeFallbackRuntime := ingress.NewClaudeFallbackRuntime(loadClaudeFallbackPolicy(st))

	ctxWatch, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	if err := pool.WatchDir(ctxWatch, chatgptDir, logger); err != nil {
		logger.Warn("pool watcher disabled", "err", err)
	}
	if err := claudePool.WatchDir(ctxWatch, claudeDir, logger); err != nil {
		logger.Warn("claude pool watcher disabled", "err", err)
	}

	wham := &control.WhamPoller{
		Pool:     pool,
		Interval: cfg.Wham.PollInterval,
		Logger:   logger,
	}

	recorder := control.NewRecorder(st, queue, pool)
	recorder.SetClaudePool(claudePool)
	refresher := &control.TokenRefresher{PoolDir: chatgptDir, Queue: queue}
	claudeRefresher := &control.ClaudeTokenRefresher{PoolDir: claudeDir, Queue: queue}

	embedded, embErr := pricing.LoadEmbedded()
	if embErr != nil {
		logger.Warn("pricing embed load failed", "err", embErr)
		embedded = &pricing.Snapshot{Models: map[string]pricing.CompactPrice{}, Origin: pricing.OriginEmbedded}
	}
	priceSrc := pricing.NewSource(embedded)
	priceFetcher := pricing.NewFetcher(priceSrc, pricing.FetcherConfig{Logger: logger})
	priceService := &pricing.Service{Source: priceSrc, Fetcher: priceFetcher}
	go priceFetcher.Run(ctxWatch)
	logger.Info("pricing loaded", "origin", embedded.Origin, "models", len(embedded.Models))

	chatgpt := provider.NewChatGPTClient()
	if v := os.Getenv("PROXYGATE_CHATGPT_BASE_URL"); v != "" {
		chatgpt.BaseURL = v
		logger.Warn("upstream base URL overridden", "url", v)
	}
	if v := os.Getenv("PROXYGATE_CHATGPT_USAGE_URL"); v != "" {
		chatgpt.UsageURL = v
	}
	if v := os.Getenv("PROXYGATE_CLAUDE_USAGE_URL"); v != "" {
		provider.ClaudeUsageURLOverride = v
	}
	wham.Client = chatgpt
	wham.Refresher = refresher
	wham.Start(ctxWatch)
	defer wham.Stop()
	claudeUsage := &control.ClaudeUsagePoller{Pool: claudePool, Refresher: claudeRefresher, Queue: queue, Interval: cfg.Wham.PollInterval, Logger: logger}
	claudeUsage.Start(ctxWatch)
	defer claudeUsage.Stop()

	openai := provider.NewOpenAIClient()
	if v := os.Getenv("PROXYGATE_OPENAI_BASE_URL"); v != "" {
		openai.BaseURL = v
	}

	proxyToken := os.Getenv("PROXYGATE_PROXY_TOKEN")
	if proxyToken == "" {
		logger.Warn("PROXYGATE_PROXY_TOKEN unset, /v1/* endpoints accept any caller")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	dashboardHTML, _ := web.Files.ReadFile("dashboard.html")
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(dashboardHTML)
	})

	responses := &ingress.ResponsesHandler{
		Pool:      pool,
		KeyPool:   keyPool,
		ChatGPT:   chatgpt,
		OpenAI:    openai,
		Recorder:  recorder,
		Refresher: refresher,
		Pricer:    priceSrc,
		Priority:  cfg.Routing.Priority,
		Logger:    logger,
	}
	responsesGated := ingress.RequireProxyToken(proxyToken, responses)
	mux.Handle("POST /v1/responses", responsesGated)
	mux.Handle("POST /v1/responses/compact", responsesGated)
	mux.Handle("POST /responses", responsesGated)
	mux.Handle("POST /responses/compact", responsesGated)

	anthropicClient := provider.NewAnthropicClient()
	if v := os.Getenv("PROXYGATE_ANTHROPIC_BASE_URL"); v != "" {
		anthropicClient.BaseURL = v
	}
	vertexClient := provider.NewAnthropicVertexClient(context.Background())
	messages := &ingress.MessagesHandler{
		ClaudePool:      claudePool,
		KeyPool:         keyPool,
		Anthropic:       anthropicClient,
		Vertex:          vertexClient,
		Recorder:        recorder,
		ClaudeRefresher: claudeRefresher,
		Pricer:          priceSrc,
		Priority:        cfg.Routing.Priority,
		FallbackRuntime: claudeFallbackRuntime,
		Logger:          logger,
	}
	mux.Handle("POST /v1/messages", ingress.RequireProxyTokenOr(proxyToken, messages, func(token string) bool {
		if token == "" || claudePool == nil {
			return false
		}
		if strings.HasPrefix(token, "sk-ant-oat") {
			return true
		}
		for _, acc := range claudePool.List() {
			if subtle.ConstantTimeCompare([]byte(token), []byte(acc.AccessToken)) == 1 {
				return true
			}
		}
		return false
	}))

	chatClient := provider.NewChatCompletionsClient()
	if v := os.Getenv("PROXYGATE_CHAT_BASE_URL"); v != "" {
		chatClient.BaseURL = v
	}
	chat := &ingress.ChatHandler{
		KeyPool:  keyPool,
		Chat:     chatClient,
		Recorder: recorder,
		Pricer:   priceSrc,
		Logger:   logger,
	}
	mux.Handle("POST /v1/chat/completions", ingress.RequireProxyToken(proxyToken, chat))

	passthrough := &ingress.PassthroughHandler{
		Pool:      pool,
		Refresher: refresher,
		BaseURL:   chatgptBaseHost(chatgpt.BaseURL),
		Logger:    logger,
	}
	modelsProxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/backend-api/codex/models"
		passthrough.ServeHTTP(w, r)
	})
	mux.Handle("GET /v1/models", ingress.RequireProxyToken(proxyToken, modelsProxy))
	mux.Handle("GET /models", ingress.RequireProxyToken(proxyToken, modelsProxy))
	mux.Handle("/backend-api/", passthrough)
	mux.Handle("/ps/", passthrough)
	mux.Handle("/hazelnuts", passthrough)
	mux.Handle("/hazelnuts/", passthrough)
	mux.Handle("/plugins/", passthrough)
	mux.Handle("/codex/", passthrough)
	mux.Handle("/api/codex/", passthrough)

	chatgptPoolDir := filepath.Join(cfg.Paths.PoolDir, "chatgpt")
	oauthStarter := func(_ context.Context) (string, error) {
		pkce, err := auth.NewPKCE()
		if err != nil {
			return "", err
		}
		state, err := auth.NewState()
		if err != nil {
			return "", err
		}
		cb, err := auth.StartOpenAICallback(context.Background(), state)
		if err != nil {
			return "", err
		}
		authURL := auth.OpenAIAuthorizeURL(cb.RedirectURI(), pkce.Challenge, state)
		go func() {
			defer cb.Close()
			waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			res, werr := cb.Wait(waitCtx)
			if werr != nil {
				logger.Warn("oauth: callback failed", "err", werr)
				return
			}
			tok, exErr := provider.ExchangeOpenAICode(waitCtx, res.Code, pkce.Verifier, cb.RedirectURI())
			if exErr != nil {
				logger.Warn("oauth: exchange failed", "err", exErr)
				return
			}
			claims, cerr := auth.ExtractAccountClaims(tok.AccessToken)
			if cerr != nil {
				logger.Warn("oauth: claims decode failed", "err", cerr)
				return
			}
			if claims.Email == "" {
				logger.Warn("oauth: empty email in claims")
				return
			}
			expires := tok.ExpiresAt
			if expires.IsZero() && !claims.ExpiresAt.IsZero() {
				expires = claims.ExpiresAt
			}
			acc := &broker.Account{
				Email:        claims.Email,
				AccountID:    claims.AccountID,
				PlanType:     broker.PlanType(strings.ToLower(claims.PlanType)),
				AccessToken:  tok.AccessToken,
				RefreshToken: tok.RefreshToken,
				IDToken:      tok.IDToken,
				ExpiresAt:    expires,
				CreatedAt:    time.Now().UTC(),
			}
			path, serr := broker.SaveAccountFile(chatgptPoolDir, acc)
			if serr != nil {
				logger.Warn("oauth: save failed", "err", serr)
				return
			}
			logger.Info("oauth: account added", "email", acc.Email, "path", path)
		}()
		return authURL, nil
	}
	claudeOAuthStarter := func(_ context.Context) (string, error) {
		pkce, err := auth.NewPKCE()
		if err != nil {
			return "", err
		}
		state, err := auth.NewState()
		if err != nil {
			return "", err
		}
		cb, err := auth.StartClaudeCallback(context.Background(), state)
		if err != nil {
			return "", err
		}
		authURL := auth.ClaudeAuthorizeURL(cb.RedirectURI(), pkce.Challenge, state)
		go func() {
			defer cb.Close()
			waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			res, werr := cb.Wait(waitCtx)
			if werr != nil {
				logger.Warn("claude oauth: callback failed", "err", werr)
				return
			}
			tok, exErr := provider.ExchangeClaudeCode(waitCtx, res.Code, pkce.Verifier, cb.RedirectURI(), res.State)
			if exErr != nil {
				logger.Warn("claude oauth: exchange failed", "err", exErr)
				return
			}
			prof, perr := provider.GetClaudeProfile(waitCtx, tok.AccessToken)
			if perr != nil {
				logger.Warn("claude oauth: profile failed", "err", perr)
				return
			}
			if prof.Email == "" {
				logger.Warn("claude oauth: empty email in profile")
				return
			}
			acc := &broker.ClaudeAccount{
				Email:            prof.Email,
				AccountID:        prof.AccountID,
				SubscriptionType: prof.SubscriptionType,
				RateLimitTier:    prof.RateLimitTier,
				AccessToken:      tok.AccessToken,
				RefreshToken:     tok.RefreshToken,
				ExpiresAt:        tok.ExpiresAt,
				CreatedAt:        time.Now().UTC(),
			}
			path, serr := broker.SaveClaudeAccountFile(claudeDir, acc)
			if serr != nil {
				logger.Warn("claude oauth: save failed", "err", serr)
				return
			}
			claudePool.Add(acc)
			logger.Info("claude oauth: account added", "email", acc.Email, "path", path)
		}()
		return authURL, nil
	}

	admin := &control.AdminAPI{
		Pool:             pool,
		ClaudePool:       claudePool,
		KeyPool:          keyPool,
		Recorder:         recorder,
		Refresher:        refresher,
		ClaudeRefresher:  claudeRefresher,
		ClaudeFallback:   claudeFallbackRuntime,
		Vertex:           vertexClient,
		Pricing:          priceService,
		Queue:            queue,
		Token:            cfg.Server.AdminToken,
		OAuthStart:       oauthStarter,
		ClaudeOAuthStart: claudeOAuthStarter,
		ChatGPTPoolDir:   chatgptPoolDir,
	}
	admin.Mount(mux)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queue.Flush()
	return srv.Shutdown(shutdownCtx)
}

func cmdAddAccount(args []string) error {
	configPath := parseConfigPath(args)
	noBrowser := false
	codeInput := ""
	for _, a := range args {
		switch {
		case a == "--no-browser":
			noBrowser = true
		case strings.HasPrefix(a, "--code="):
			codeInput = strings.TrimPrefix(a, "--code=")
		}
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	chatgptDir := cfg.PoolSubdir("chatgpt")
	if err := os.MkdirAll(chatgptDir, 0o700); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if codeInput != "" {
		acc, err := cmd.AddAccountFromCode(ctx, chatgptDir, codeInput)
		if err != nil {
			return err
		}
		fmt.Println("added:", acc.Email)
		return nil
	}

	acc, err := cmd.AddAccountInteractive(ctx, chatgptDir, !noBrowser)
	if err != nil {
		return err
	}
	fmt.Println("added:", acc.Email)
	return nil
}

func cmdStatus(args []string) error {
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	chatgptDir := cfg.PoolSubdir("chatgpt")
	claudeDir := cfg.PoolSubdir("claude")
	keyDir := cfg.PoolSubdir("apikeys")
	accs, _ := cmd.ListAccounts(chatgptDir)
	claudeAccs, _ := cmd.ListAccounts(claudeDir)
	keys, _ := cmd.ListAccounts(keyDir)
	fmt.Printf("addr:        %s\n", cfg.Server.Addr)
	fmt.Printf("data_dir:    %s\n", cfg.Paths.DataDir)
	fmt.Printf("pool_dir:    %s\n", cfg.Paths.PoolDir)
	fmt.Printf("priority:    %s\n", cfg.Routing.Priority)
	fmt.Printf("accounts:    %d\n", len(accs))
	fmt.Printf("claude_accounts: %d\n", len(claudeAccs))
	fmt.Printf("api_keys:    %d\n", len(keys))
	return nil
}

func cmdAddClaudeAccount(args []string) error {
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}

	from := flagValue(args, "--from=")
	noBrowser := false
	codeInput := ""
	abortPending := false
	for _, a := range args {
		switch {
		case a == "--no-browser":
			noBrowser = true
		case strings.HasPrefix(a, "--code="):
			codeInput = strings.TrimPrefix(a, "--code=")
		case a == "--abort-pending":
			abortPending = true
		}
	}

	if abortPending {
		if err := cmd.DeletePending(cfg.Paths.DataDir); err != nil {
			return err
		}
		fmt.Println("pending Claude OAuth cleared")
		return nil
	}

	if noBrowser {
		return cmd.AddClaudeAccountNoBrowser(cfg.Paths.DataDir)
	}

	if codeInput != "" {
		dir := cfg.PoolSubdir("claude")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		acc, err := cmd.AddClaudeAccountFromCode(ctx, cfg.Paths.DataDir, dir, codeInput, flagValue(args, "--email="))
		if err != nil {
			return err
		}
		fmt.Printf("added: %s (account_id=%s, plan=%s)\n", acc.Email, acc.AccountID, acc.SubscriptionType)
		return nil
	}

	if from != "" {
		dir := cfg.PoolSubdir("claude")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		acc, err := cmd.ImportClaudeAccount(from, dir, flagValue(args, "--email="))
		if err != nil {
			return err
		}
		fmt.Println("added claude:", acc.Email)
		return nil
	}

	return fmt.Errorf("usage: add-claude-account --from=PATH | --no-browser | --code=URL")
}

func cmdDisable(args []string, disable bool) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("require an account email or key id")
	}
	target := args[0]
	cfg, err := LoadConfig(parseConfigPath(args[1:]))
	if err != nil {
		return err
	}
	chatgptDir := cfg.PoolSubdir("chatgpt")
	claudeDir := cfg.PoolSubdir("claude")
	keyDir := cfg.PoolSubdir("apikeys")

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	queue := control.NewQueue(st)
	defer queue.Close()

	accPool := broker.NewPool(broker.PoolConfig{})
	if err := accPool.LoadDir(chatgptDir); err != nil {
		return err
	}
	keyPool := broker.NewAPIKeyPool()
	if err := keyPool.LoadDir(keyDir); err != nil {
		return err
	}
	claudePool := broker.NewClaudePool()
	if err := claudePool.LoadDir(claudeDir); err != nil {
		return err
	}

	if acc, ok := accPool.Get(target); ok {
		acc.SetDisabled(disable)
		stats := acc.Stats()
		raw, err := json.Marshal(stats)
		if err == nil {
			_ = queue.Put(store.BucketAccounts, "stats:"+target, raw)
		}
		queue.Flush()
		fmt.Printf("account %s disabled=%v\n", target, disable)
		return nil
	}
	if k, ok := keyPool.Get(target); ok {
		k.SetDisabled(disable)
		stats := k.Stats()
		raw, err := json.Marshal(stats)
		if err == nil {
			_ = queue.Put(store.BucketAPIKeys, "stats:"+target, raw)
		}
		queue.Flush()
		fmt.Printf("key %s disabled=%v\n", target, disable)
		return nil
	}
	if acc, ok := claudePool.Get(target); ok {
		acc.SetDisabled(disable)
		stats := acc.Stats()
		raw, err := json.Marshal(stats)
		if err == nil {
			_ = queue.Put(store.BucketClaudeAccounts, "stats:"+target, raw)
		}
		queue.Flush()
		fmt.Printf("claude account %s disabled=%v\n", target, disable)
		return nil
	}
	return fmt.Errorf("not found: %s", target)
}

func cmdImport(args []string) error {
	from := flagValue(args, "--from=")
	if from == "" {
		return fmt.Errorf("--from=PATH required")
	}
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	dir := cfg.PoolSubdir("chatgpt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	imported, err := cmd.ImportV1Accounts(from, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d accounts to %s\n", imported, dir)
	return nil
}

func cmdImportKeys(args []string) error {
	from := flagValue(args, "--from=")
	if from == "" {
		return fmt.Errorf("--from=PATH required")
	}
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	dir := cfg.PoolSubdir("apikeys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	imported, err := cmd.ImportV1APIKeys(from, dir)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d keys to %s\n", imported, dir)
	return nil
}

func flagValue(args []string, prefix string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

func cmdList(args []string) error {
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	chatgptDir := cfg.PoolSubdir("chatgpt")
	names, err := cmd.ListAccounts(chatgptDir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("(no accounts in", chatgptDir+")")
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func cmdListClaude(args []string) error {
	cfg, err := LoadConfig(parseConfigPath(args))
	if err != nil {
		return err
	}
	claudeDir := cfg.PoolSubdir("claude")
	names, err := cmd.ListAccounts(claudeDir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("(no accounts in", claudeDir+")")
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func chatgptBaseHost(fullURL string) string {
	const defaultBase = "https://chatgpt.com"
	idx := strings.Index(fullURL, "/backend-api/")
	if idx > 0 {
		return fullURL[:idx]
	}
	if strings.HasPrefix(fullURL, "http") {
		parts := strings.SplitN(fullURL, "/", 4)
		if len(parts) >= 3 {
			return parts[0] + "//" + parts[2]
		}
	}
	return defaultBase
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func loadAccountStats(st *store.Store, pool *broker.Pool) {
	for _, acc := range pool.List() {
		raw, err := st.Get(store.BucketAccounts, "stats:"+acc.Email)
		if err != nil {
			continue
		}
		var s broker.AccountStats
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		acc.ApplyStats(s)
	}
}

func loadKeyStats(st *store.Store, pool *broker.APIKeyPool) {
	for _, k := range pool.List() {
		raw, err := st.Get(store.BucketAPIKeys, "stats:"+k.ID)
		if err != nil {
			continue
		}
		var s broker.APIKeyStats
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		k.ApplyStats(s)
	}
}

func loadClaudeStats(st *store.Store, pool *broker.ClaudePool) {
	for _, acc := range pool.List() {
		raw, err := st.Get(store.BucketClaudeAccounts, "stats:"+acc.Email)
		if err != nil {
			continue
		}
		var s broker.ClaudeAccountStats
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		acc.ApplyStats(s)
	}
}

func loadClaudeFallbackPolicy(st *store.Store) ingress.ClaudeFallbackPolicy {
	raw, err := st.Get(store.BucketSettings, control.ClaudeFallbackSettingsKey)
	if err != nil {
		return ingress.DefaultClaudeFallbackPolicy()
	}
	policy, err := ingress.ParseClaudeFallbackPolicy(raw)
	if err != nil {
		return ingress.DefaultClaudeFallbackPolicy()
	}
	return policy
}
