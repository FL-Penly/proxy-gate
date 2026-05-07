package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/codeking-ai/cligate-v2/broker"
	"github.com/codeking-ai/cligate-v2/ingress"
	"github.com/codeking-ai/cligate-v2/pricing"
	"github.com/codeking-ai/cligate-v2/provider"
	"github.com/codeking-ai/cligate-v2/store"
)

type DailyUsage struct {
	Date         string  `json:"date"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	CachedTokens int64   `json:"cached_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	Errors       int64   `json:"errors"`
	Unpriced     int64   `json:"unpriced,omitempty"`
}

type BillingDailyUsage struct {
	DailyUsage
	Model       string `json:"model,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
}

type Recorder struct {
	store *store.Store
	queue *Queue
	pool  *broker.Pool

	mu               sync.Mutex
	day              map[string]*DailyUsage
	byAccount        map[string]map[string]*DailyUsage
	byKey            map[string]map[string]*DailyUsage
	byAccountBilling map[string]map[string]*BillingDailyUsage
	byKeyBilling     map[string]map[string]*BillingDailyUsage
}

func NewRecorder(s *store.Store, q *Queue, p *broker.Pool) *Recorder {
	return &Recorder{
		store:            s,
		queue:            q,
		pool:             p,
		day:              make(map[string]*DailyUsage),
		byAccount:        make(map[string]map[string]*DailyUsage),
		byKey:            make(map[string]map[string]*DailyUsage),
		byAccountBilling: make(map[string]map[string]*BillingDailyUsage),
		byKeyBilling:     make(map[string]map[string]*BillingDailyUsage),
	}
}

func (r *Recorder) RecordRequest(rec ingress.UsageRecord) {
	now := time.Now()
	dayKey := now.Format("2006-01-02")
	billing := billingKeyOf(rec.Model, rec.ServiceTier)

	r.mu.Lock()
	d, ok := r.day[dayKey]
	if !ok {
		d = r.loadDay(dayKey, "day:"+dayKey)
		r.day[dayKey] = d
	}
	bumpDay(d, &rec)
	snap := *d

	var perAccountSnap *DailyUsage
	var perKeySnap *DailyUsage
	var perAccountBillingSnap *BillingDailyUsage
	var perKeyBillingSnap *BillingDailyUsage
	if rec.Account != "" {
		perAccountSnap = r.bumpSubLocked(r.byAccount, dayKey, rec.Account, "day:"+dayKey+":acc:"+rec.Account, &rec)
	}
	if rec.KeyID != "" {
		perKeySnap = r.bumpSubLocked(r.byKey, dayKey, rec.KeyID, "day:"+dayKey+":key:"+rec.KeyID, &rec)
	}
	if billing != "" {
		if rec.Account != "" {
			perAccountBillingSnap = r.bumpBillingLocked(r.byAccountBilling, dayKey, rec.Account, billing, "day:"+dayKey+":acc:"+rec.Account+":bk:"+billing, &rec)
		}
		if rec.KeyID != "" {
			perKeyBillingSnap = r.bumpBillingLocked(r.byKeyBilling, dayKey, rec.KeyID, billing, "day:"+dayKey+":key:"+rec.KeyID+":bk:"+billing, &rec)
		}
	}
	r.mu.Unlock()

	if data, err := json.Marshal(snap); err == nil {
		_ = r.queue.Put(store.BucketUsage, "day:"+dayKey, data)
	}
	if perAccountSnap != nil {
		if data, err := json.Marshal(*perAccountSnap); err == nil {
			_ = r.queue.Put(store.BucketUsage, "day:"+dayKey+":acc:"+rec.Account, data)
		}
	}
	if perKeySnap != nil {
		if data, err := json.Marshal(*perKeySnap); err == nil {
			_ = r.queue.Put(store.BucketUsage, "day:"+dayKey+":key:"+rec.KeyID, data)
		}
	}
	if perAccountBillingSnap != nil {
		if data, err := json.Marshal(*perAccountBillingSnap); err == nil {
			_ = r.queue.Put(store.BucketUsage, "day:"+dayKey+":acc:"+rec.Account+":bk:"+billing, data)
		}
	}
	if perKeyBillingSnap != nil {
		if data, err := json.Marshal(*perKeyBillingSnap); err == nil {
			_ = r.queue.Put(store.BucketUsage, "day:"+dayKey+":key:"+rec.KeyID+":bk:"+billing, data)
		}
	}

	if rec.Account != "" && r.pool != nil {
		if acc, ok := r.pool.Get(rec.Account); ok {
			if data, err := json.Marshal(acc.Stats()); err == nil {
				_ = r.queue.Put(store.BucketAccounts, "stats:"+rec.Account, data)
			}
		}
	}
}

func bumpDay(d *DailyUsage, rec *ingress.UsageRecord) {
	d.Requests++
	d.InputTokens += rec.InputTokens
	d.CachedTokens += rec.CachedTokens
	d.OutputTokens += rec.OutputTokens
	d.TotalTokens += rec.TotalTokens
	d.TotalCost += rec.Cost
	if !rec.Success {
		d.Errors++
	}
	if rec.CostUnpriced {
		d.Unpriced++
	}
}

func (r *Recorder) bumpSubLocked(m map[string]map[string]*DailyUsage, dayKey, id, subKey string, rec *ingress.UsageRecord) *DailyUsage {
	bucket, ok := m[dayKey]
	if !ok {
		bucket = make(map[string]*DailyUsage)
		m[dayKey] = bucket
	}
	d, ok := bucket[id]
	if !ok {
		d = r.loadDay(dayKey, subKey)
		bucket[id] = d
	}
	bumpDay(d, rec)
	snap := *d
	return &snap
}

func (r *Recorder) bumpBillingLocked(m map[string]map[string]*BillingDailyUsage, dayKey, id, billing, subKey string, rec *ingress.UsageRecord) *BillingDailyUsage {
	bucket, ok := m[dayKey]
	if !ok {
		bucket = make(map[string]*BillingDailyUsage)
		m[dayKey] = bucket
	}
	mapKey := id + "\x00" + billing
	d, ok := bucket[mapKey]
	if !ok {
		d = r.loadBillingDay(dayKey, subKey, rec.Model, rec.ServiceTier)
		bucket[mapKey] = d
	}
	bumpDay(&d.DailyUsage, rec)
	snap := *d
	return &snap
}

func (r *Recorder) Today() DailyUsage {
	dayKey := time.Now().Format("2006-01-02")
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.day[dayKey]
	if !ok {
		d = r.loadDay(dayKey, "day:"+dayKey)
		r.day[dayKey] = d
	}
	return *d
}

func (r *Recorder) TodayByAccount() map[string]DailyUsage {
	dayKey := time.Now().Format("2006-01-02")
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]DailyUsage)
	if bucket, ok := r.byAccount[dayKey]; ok {
		for email, d := range bucket {
			out[email] = *d
		}
	}
	for _, email := range r.scanPrefix("day:" + dayKey + ":acc:") {
		if strings.Contains(email, ":bk:") {
			continue
		}
		if _, exists := out[email]; exists {
			continue
		}
		key := "day:" + dayKey + ":acc:" + email
		raw, err := r.store.Get(store.BucketUsage, key)
		if err != nil || raw == nil {
			continue
		}
		var d DailyUsage
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		d.Date = dayKey
		out[email] = d
	}
	return out
}

func (r *Recorder) TodayByKey() map[string]DailyUsage {
	dayKey := time.Now().Format("2006-01-02")
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]DailyUsage)
	if bucket, ok := r.byKey[dayKey]; ok {
		for id, d := range bucket {
			out[id] = *d
		}
	}
	for _, id := range r.scanPrefix("day:" + dayKey + ":key:") {
		if strings.Contains(id, ":bk:") {
			continue
		}
		if _, exists := out[id]; exists {
			continue
		}
		key := "day:" + dayKey + ":key:" + id
		raw, err := r.store.Get(store.BucketUsage, key)
		if err != nil || raw == nil {
			continue
		}
		var d DailyUsage
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		d.Date = dayKey
		out[id] = d
	}
	return out
}

func (r *Recorder) TodayByAccountBilling() map[string]map[string]BillingDailyUsage {
	return r.todayByBilling(r.byAccountBilling, "day:"+time.Now().Format("2006-01-02")+":acc:")
}

func (r *Recorder) TodayByKeyBilling() map[string]map[string]BillingDailyUsage {
	return r.todayByBilling(r.byKeyBilling, "day:"+time.Now().Format("2006-01-02")+":key:")
}

func (r *Recorder) todayByBilling(mem map[string]map[string]*BillingDailyUsage, prefix string) map[string]map[string]BillingDailyUsage {
	dayKey := time.Now().Format("2006-01-02")
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]map[string]BillingDailyUsage)
	add := func(id, billing string, d BillingDailyUsage) {
		bucket, ok := out[id]
		if !ok {
			bucket = make(map[string]BillingDailyUsage)
			out[id] = bucket
		}
		bucket[billing] = d
	}

	if bucket, ok := mem[dayKey]; ok {
		for mapKey, d := range bucket {
			id, billing := splitBillingMapKey(mapKey)
			add(id, billing, *d)
		}
	}

	_ = r.store.ForEach(store.BucketUsage, func(k, v []byte) error {
		ks := string(k)
		if !strings.HasPrefix(ks, prefix) {
			return nil
		}
		rest := ks[len(prefix):]
		id, billing, ok := strings.Cut(rest, ":bk:")
		if !ok {
			return nil
		}
		if bucket, ok := out[id]; ok {
			if _, exists := bucket[billing]; exists {
				return nil
			}
		}
		var d BillingDailyUsage
		if err := json.Unmarshal(v, &d); err != nil {
			return nil
		}
		d.Date = dayKey
		if d.Model == "" {
			d.Model, d.ServiceTier = parseBillingKey(billing)
		}
		add(id, billing, d)
		return nil
	})
	return out
}

func (r *Recorder) scanPrefix(prefix string) []string {
	var ids []string
	_ = r.store.ForEach(store.BucketUsage, func(k, _ []byte) error {
		ks := string(k)
		if strings.HasPrefix(ks, prefix) {
			ids = append(ids, ks[len(prefix):])
		}
		return nil
	})
	return ids
}

func (r *Recorder) loadDay(dayKey, key string) *DailyUsage {
	d := &DailyUsage{Date: dayKey}
	raw, err := r.store.Get(store.BucketUsage, key)
	if err != nil {
		return d
	}
	_ = json.Unmarshal(raw, d)
	d.Date = dayKey
	return d
}

func (r *Recorder) loadBillingDay(dayKey, key, model, tier string) *BillingDailyUsage {
	d := &BillingDailyUsage{DailyUsage: DailyUsage{Date: dayKey}, Model: model, ServiceTier: tier}
	raw, err := r.store.Get(store.BucketUsage, key)
	if err != nil {
		return d
	}
	_ = json.Unmarshal(raw, d)
	d.Date = dayKey
	if d.Model == "" {
		d.Model = model
	}
	if d.ServiceTier == "" {
		d.ServiceTier = tier
	}
	return d
}

func billingKeyOf(model, tier string) string {
	return pricing.BillingKey(model, tier)
}

func parseBillingKey(billing string) (model, tier string) {
	if m, t, ok := strings.Cut(billing, "@"); ok {
		return m, t
	}
	return billing, ""
}

func splitBillingMapKey(mapKey string) (id, billing string) {
	if i, b, ok := strings.Cut(mapKey, "\x00"); ok {
		return i, b
	}
	return mapKey, ""
}

type TokenRefresher struct {
	PoolDir string
	Queue   *Queue
}

func (t *TokenRefresher) RefreshToken(ctx context.Context, acc *broker.Account) error {
	if acc.RefreshToken == "" {
		return errors.New("refresh: no refresh_token")
	}
	tok, err := provider.RefreshOpenAIToken(ctx, acc.RefreshToken)
	if err != nil {
		return err
	}
	expires := tok.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(time.Hour)
	}
	acc.UpdateTokens(tok.AccessToken, tok.RefreshToken, tok.IDToken, expires)
	if t.PoolDir != "" {
		if _, err := broker.SaveAccountFile(t.PoolDir, acc); err != nil {
			return err
		}
	}
	return nil
}
