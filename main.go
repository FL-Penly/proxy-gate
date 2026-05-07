package main

import (
	"context"
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
	case "list":
		return cmdList(args)
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
  list [--config=PATH]                  List accounts in pool/chatgpt/
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

	loadAccountStats(st, pool)
	loadKeyStats(st, keyPool)

	ctxWatch, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	if err := pool.WatchDir(ctxWatch, chatgptDir, logger); err != nil {
		logger.Warn("pool watcher disabled", "err", err)
	}

	wham := &control.WhamPoller{
		Pool:     pool,
		Interval: cfg.Wham.PollInterval,
		Logger:   logger,
	}

	recorder := control.NewRecorder(st, queue, pool)
	refresher := &control.TokenRefresher{PoolDir: chatgptDir, Queue: queue}

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
	wham.Client = chatgpt
	wham.Refresher = refresher
	wham.Start(ctxWatch)
	defer wham.Stop()

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
	messages := &ingress.MessagesHandler{
		KeyPool:   keyPool,
		Anthropic: anthropicClient,
		Recorder:  recorder,
		Logger:    logger,
	}
	mux.Handle("POST /v1/messages", ingress.RequireProxyToken(proxyToken, messages))

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

	admin := &control.AdminAPI{
		Pool:       pool,
		KeyPool:    keyPool,
		Recorder:   recorder,
		Refresher:  refresher,
		Pricing:    priceService,
		Queue:      queue,
		Token:      cfg.Server.AdminToken,
		OAuthStart: oauthStarter,
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
	keyDir := cfg.PoolSubdir("apikeys")
	accs, _ := cmd.ListAccounts(chatgptDir)
	keys, _ := cmd.ListAccounts(keyDir)
	fmt.Printf("addr:        %s\n", cfg.Server.Addr)
	fmt.Printf("data_dir:    %s\n", cfg.Paths.DataDir)
	fmt.Printf("pool_dir:    %s\n", cfg.Paths.PoolDir)
	fmt.Printf("priority:    %s\n", cfg.Routing.Priority)
	fmt.Printf("accounts:    %d\n", len(accs))
	fmt.Printf("api_keys:    %d\n", len(keys))
	return nil
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
