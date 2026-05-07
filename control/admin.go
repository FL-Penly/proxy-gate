package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/pricing"
	"github.com/codeking-ai/cligate-v2/store"
)

const sessionCookie = "cligate_admin"

type OAuthStarter func(ctx context.Context) (authURL string, err error)

type PricingService interface {
	Status() pricing.StatusReport
	Refresh(ctx context.Context) error
	Misses() map[string]int64
}

type AdminAPI struct {
	Pool       *broker.Pool
	KeyPool    *broker.APIKeyPool
	Recorder   *Recorder
	Refresher  *TokenRefresher
	Pricing    PricingService
	Queue      *Queue
	Token      string
	OAuthStart OAuthStarter
}

type accountSummary struct {
	Email             string  `json:"email"`
	AccountID         string  `json:"account_id"`
	PlanType          string  `json:"plan_type"`
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTkn     int64   `json:"total_input_tokens"`
	TotalOutputTkn    int64   `json:"total_output_tokens"`
	TotalCost         float64 `json:"total_cost"`
	Disabled          bool    `json:"disabled"`
	Dead              bool    `json:"dead"`
	DeadReason        string  `json:"dead_reason,omitempty"`
	PrimaryUsedPct    float64 `json:"primary_used_pct,omitempty"`
	SecondaryUsedPct  float64 `json:"secondary_used_pct,omitempty"`
	PrimaryResetAt    string  `json:"primary_reset_at,omitempty"`
	SecondaryResetAt  string  `json:"secondary_reset_at,omitempty"`
	CooldownUntil     string  `json:"cooldown_until,omitempty"`
	LastUsed          string  `json:"last_used,omitempty"`
	LastWhamAt        string  `json:"last_wham_at,omitempty"`
	WhamAgeSeconds    int64   `json:"wham_age_seconds,omitempty"`
	WhamFailCount     int     `json:"wham_fail_count,omitempty"`
	WhamFailing       bool    `json:"wham_failing,omitempty"`
	WhamLimitReached  bool    `json:"wham_limit_reached,omitempty"`
	LastWhamErr       string  `json:"last_wham_err,omitempty"`
}

type RefreshFunc func(email string) error

func (a *AdminAPI) Mount(mux *http.ServeMux) {
	mux.Handle("POST /admin/ui/login", http.HandlerFunc(a.uiLogin))
	mux.Handle("POST /admin/ui/logout", http.HandlerFunc(a.uiLogout))
	mux.Handle("POST /admin/ui/oauth-start", a.gate(a.uiOAuthStart))
	mux.Handle("GET /admin/accounts", a.gate(a.listAccounts))
	mux.Handle("GET /admin/usage", a.gate(a.usage))
	mux.Handle("GET /admin/status", a.gate(a.status))
	mux.Handle("POST /admin/accounts/{email}/disable", a.gate(a.toggleAccount(true)))
	mux.Handle("POST /admin/accounts/{email}/enable", a.gate(a.toggleAccount(false)))
	mux.Handle("POST /admin/accounts/{email}/refresh", a.gate(a.refreshAccount))
	mux.Handle("DELETE /admin/accounts/{email}", a.gate(a.deleteAccount))
	mux.Handle("GET /admin/keys", a.gate(a.listKeys))
	mux.Handle("POST /admin/keys/{id}/disable", a.gate(a.toggleKey(true)))
	mux.Handle("POST /admin/keys/{id}/enable", a.gate(a.toggleKey(false)))
	mux.Handle("DELETE /admin/keys/{id}", a.gate(a.deleteKey))
	mux.Handle("GET /admin/usage/cost", a.gate(a.usageCost))
	mux.Handle("GET /admin/pricing/status", a.gate(a.pricingStatus))
	mux.Handle("POST /admin/pricing/refresh", a.gate(a.pricingRefresh))
	mux.Handle("GET /admin/pricing/unpriced", a.gate(a.pricingUnpriced))
}

func (a *AdminAPI) uiLogin(w http.ResponseWriter, r *http.Request) {
	if a.Token == "" {
		http.Error(w, "admin token not configured", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(string(body)) != a.Token {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 30,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *AdminAPI) uiOAuthStart(w http.ResponseWriter, r *http.Request) {
	if a.OAuthStart == nil {
		http.Error(w, "oauth not configured", http.StatusServiceUnavailable)
		return
	}
	authURL, err := a.OAuthStart(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

func (a *AdminAPI) uiLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *AdminAPI) refreshAccount(w http.ResponseWriter, r *http.Request) {
	if a.Refresher == nil {
		http.Error(w, "refresher not configured", http.StatusServiceUnavailable)
		return
	}
	email := r.PathValue("email")
	acc, ok := a.Pool.Get(email)
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if err := a.Refresher.RefreshToken(r.Context(), acc); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "refreshed_at": acc.ExpiresAt})
}

func (a *AdminAPI) deleteAccount(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if _, ok := a.Pool.Get(email); !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	a.Pool.Remove(email)
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "removed": true})
}

func (a *AdminAPI) deleteKey(w http.ResponseWriter, r *http.Request) {
	if a.KeyPool == nil {
		http.Error(w, "no key pool", http.StatusNotFound)
		return
	}
	id := r.PathValue("id")
	if _, ok := a.KeyPool.Get(id); !ok {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	a.KeyPool.Remove(id)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "removed": true})
}

func (a *AdminAPI) gate(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Token == "" {
			http.Error(w, "admin token not configured", http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Admin-Token") == a.Token {
			h(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value == a.Token {
			h(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (a *AdminAPI) listAccounts(w http.ResponseWriter, _ *http.Request) {
	accs := a.Pool.List()
	now := time.Now()
	out := make([]accountSummary, 0, len(accs))
	for _, acc := range accs {
		s := acc.Stats()
		var ageSec int64
		if !s.LastWhamAt.IsZero() {
			ageSec = int64(now.Sub(s.LastWhamAt).Seconds())
		}
		out = append(out, accountSummary{
			Email:            acc.Email,
			AccountID:        acc.AccountID,
			PlanType:         string(acc.PlanType),
			TotalRequests:    s.TotalRequests,
			TotalInputTkn:    s.TotalInputTkn,
			TotalOutputTkn:   s.TotalOutputTkn,
			TotalCost:        s.TotalCost,
			Disabled:         s.Disabled,
			Dead:             s.Dead,
			DeadReason:       s.DeadReason,
			PrimaryUsedPct:   s.PrimaryUsedPct,
			SecondaryUsedPct: s.SecondaryUsedPct,
			PrimaryResetAt:   timeOrEmpty(s.PrimaryResetAt),
			SecondaryResetAt: timeOrEmpty(s.SecondaryResetAt),
			CooldownUntil:    futureTimeOrEmpty(s.CooldownUntil, now),
			LastUsed:         timeOrEmpty(s.LastUsed),
			LastWhamAt:       timeOrEmpty(s.LastWhamAt),
			WhamAgeSeconds:   ageSec,
			WhamFailCount:    s.WhamFailCount,
			WhamFailing:      s.WhamFailCount >= 3,
			WhamLimitReached: s.WhamLimitReached,
			LastWhamErr:      s.LastWhamErr,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminAPI) usage(w http.ResponseWriter, _ *http.Request) {
	if a.Recorder == nil {
		writeJSON(w, http.StatusOK, map[string]any{"today": DailyUsage{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"today":      a.Recorder.Today(),
		"by_account": a.Recorder.TodayByAccount(),
		"by_key":     a.Recorder.TodayByKey(),
	})
}

func (a *AdminAPI) usageCost(w http.ResponseWriter, _ *http.Request) {
	if a.Recorder == nil {
		writeJSON(w, http.StatusOK, costReport{})
		return
	}
	byAccount := a.Recorder.TodayByAccount()
	byKey := a.Recorder.TodayByKey()
	byAccountBilling := a.Recorder.TodayByAccountBilling()
	byKeyBilling := a.Recorder.TodayByKeyBilling()

	report := costReport{
		Today:               a.Recorder.Today(),
		ByAccount:           flattenAccountTotals(byAccount, byAccountBilling),
		ByKey:               flattenKeyTotals(byKey, byKeyBilling),
	}
	for _, m := range report.ByAccount {
		report.EquivalentCost += m.TotalCost
	}
	for _, m := range report.ByKey {
		report.RealCost += m.TotalCost
	}
	writeJSON(w, http.StatusOK, report)
}

type costRow struct {
	ID          string             `json:"id"`
	TotalCost   float64            `json:"total_cost"`
	Requests    int64              `json:"requests"`
	InputTokens int64              `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	Unpriced    int64              `json:"unpriced,omitempty"`
	ByModel     []BillingDailyUsage `json:"by_model,omitempty"`
}

type costReport struct {
	Today          DailyUsage `json:"today"`
	RealCost       float64    `json:"real_cost"`
	EquivalentCost float64    `json:"equivalent_cost"`
	ByAccount      []costRow  `json:"by_account"`
	ByKey          []costRow  `json:"by_key"`
}

func flattenAccountTotals(byAccount map[string]DailyUsage, byBilling map[string]map[string]BillingDailyUsage) []costRow {
	rows := make([]costRow, 0, len(byAccount))
	for email, d := range byAccount {
		row := costRow{
			ID:           email,
			TotalCost:    d.TotalCost,
			Requests:     d.Requests,
			InputTokens:  d.InputTokens,
			OutputTokens: d.OutputTokens,
			Unpriced:     d.Unpriced,
		}
		if bucket, ok := byBilling[email]; ok {
			models := make([]BillingDailyUsage, 0, len(bucket))
			for _, m := range bucket {
				models = append(models, m)
			}
			sort.Slice(models, func(i, j int) bool { return models[i].TotalCost > models[j].TotalCost })
			row.ByModel = models
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func flattenKeyTotals(byKey map[string]DailyUsage, byBilling map[string]map[string]BillingDailyUsage) []costRow {
	rows := make([]costRow, 0, len(byKey))
	for id, d := range byKey {
		row := costRow{
			ID:           id,
			TotalCost:    d.TotalCost,
			Requests:     d.Requests,
			InputTokens:  d.InputTokens,
			OutputTokens: d.OutputTokens,
			Unpriced:     d.Unpriced,
		}
		if bucket, ok := byBilling[id]; ok {
			models := make([]BillingDailyUsage, 0, len(bucket))
			for _, m := range bucket {
				models = append(models, m)
			}
			sort.Slice(models, func(i, j int) bool { return models[i].TotalCost > models[j].TotalCost })
			row.ByModel = models
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func (a *AdminAPI) pricingStatus(w http.ResponseWriter, _ *http.Request) {
	if a.Pricing == nil {
		writeJSON(w, http.StatusOK, pricing.StatusReport{Origin: "disabled"})
		return
	}
	writeJSON(w, http.StatusOK, a.Pricing.Status())
}

func (a *AdminAPI) pricingRefresh(w http.ResponseWriter, r *http.Request) {
	if a.Pricing == nil {
		http.Error(w, "pricing service not configured", http.StatusServiceUnavailable)
		return
	}
	if err := a.Pricing.Refresh(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "status": a.Pricing.Status()})
		return
	}
	writeJSON(w, http.StatusOK, a.Pricing.Status())
}

func (a *AdminAPI) pricingUnpriced(w http.ResponseWriter, _ *http.Request) {
	if a.Pricing == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	misses := a.Pricing.Misses()
	type row struct {
		BillingKey string `json:"billing_key"`
		Count      int64  `json:"count"`
	}
	out := make([]row, 0, len(misses))
	for k, v := range misses {
		out = append(out, row{BillingKey: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminAPI) status(w http.ResponseWriter, _ *http.Request) {
	keys := 0
	if a.KeyPool != nil {
		keys = a.KeyPool.Len()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": a.Pool.Len(),
		"api_keys": keys,
	})
}

type keySummary struct {
	ID             string  `json:"id"`
	Name           string  `json:"name,omitempty"`
	Type           string  `json:"type"`
	BaseURL        string  `json:"base_url,omitempty"`
	TotalRequests  int64   `json:"total_requests"`
	TotalInputTkn  int64   `json:"total_input_tokens"`
	TotalOutputTkn int64   `json:"total_output_tokens"`
	TotalCost      float64 `json:"total_cost"`
	Disabled       bool    `json:"disabled,omitempty"`
	Dead           bool    `json:"dead,omitempty"`
	DeadReason     string  `json:"dead_reason,omitempty"`
	CooldownUntil  string  `json:"cooldown_until,omitempty"`
}

func (a *AdminAPI) listKeys(w http.ResponseWriter, _ *http.Request) {
	if a.KeyPool == nil {
		writeJSON(w, http.StatusOK, []keySummary{})
		return
	}
	keys := a.KeyPool.List()
	now := time.Now()
	out := make([]keySummary, 0, len(keys))
	for _, k := range keys {
		s := k.Stats()
		out = append(out, keySummary{
			ID:             k.ID,
			Name:           k.Name,
			Type:           string(k.Type),
			BaseURL:        k.BaseURL,
			TotalRequests:  s.TotalRequests,
			TotalInputTkn:  s.TotalInputTkn,
			TotalOutputTkn: s.TotalOutputTkn,
			TotalCost:      s.TotalCost,
			Disabled:       s.Disabled,
			Dead:           s.Dead,
			DeadReason:     s.DeadReason,
			CooldownUntil:  futureTimeOrEmpty(s.CooldownUntil, now),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *AdminAPI) toggleKey(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.KeyPool == nil {
			http.Error(w, "no key pool", http.StatusNotFound)
			return
		}
		id := r.PathValue("id")
		k, ok := a.KeyPool.Get(id)
		if !ok {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		k.SetDisabled(disabled)
		a.persistKeyStats(k)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "disabled": disabled})
	}
}

func (a *AdminAPI) toggleAccount(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.PathValue("email")
		acc, ok := a.Pool.Get(email)
		if !ok {
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
		acc.SetDisabled(disabled)
		a.persistAccountStats(acc)
		writeJSON(w, http.StatusOK, map[string]any{"email": email, "disabled": disabled})
	}
}

func (a *AdminAPI) persistKeyStats(k *broker.APIKey) {
	if a.Queue == nil {
		return
	}
	data, err := json.Marshal(k.Stats())
	if err != nil {
		return
	}
	_ = a.Queue.Put(store.BucketAPIKeys, "stats:"+k.ID, data)
}

func (a *AdminAPI) persistAccountStats(acc *broker.Account) {
	if a.Queue == nil {
		return
	}
	data, err := json.Marshal(acc.Stats())
	if err != nil {
		return
	}
	_ = a.Queue.Put(store.BucketAccounts, "stats:"+acc.Email, data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func timeOrEmpty(t interface{ IsZero() bool; Format(string) string }) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

func futureTimeOrEmpty(t time.Time, now time.Time) string {
	if t.IsZero() || !t.After(now) {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}
