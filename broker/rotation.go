package broker

import (
	"sort"
	"time"
)

type ScoreWeights struct {
	DrainMultiplier  float64
	PrimaryBonus     float64
	InflightPenalty  float64
}

func DefaultScoreWeights() ScoreWeights {
	return ScoreWeights{
		DrainMultiplier:  1.0,
		PrimaryBonus:     0.1,
		InflightPenalty:  0.5,
	}
}

func planTier(p PlanType) float64 {
	switch p {
	case PlanPro:
		return 10.0
	case PlanEnterprise:
		return 8.0
	case PlanPlus:
		return 5.0
	case PlanTeam:
		return 3.0
	}
	return 0.0
}

func score(acc *Account, inflight int64, w ScoreWeights) float64 {
	st := acc.state.Load()
	tier := planTier(acc.PlanType)
	if st == nil {
		return tier
	}
	primaryRem := 1.0 - st.PrimaryUsedPct
	secondaryRem := 1.0 - st.SecondaryUsedPct
	if primaryRem < 0 {
		primaryRem = 0
	}
	if secondaryRem < 0 {
		secondaryRem = 0
	}
	return tier + secondaryRem*w.DrainMultiplier + primaryRem*w.PrimaryBonus - float64(inflight)*w.InflightPenalty
}

type ranked struct {
	entry *accountEntry
	score float64
}

func rankCandidates(now time.Time, p *Pool, w ScoreWeights, exclude func(*Account) bool) []ranked {
	out := make([]ranked, 0, len(p.accounts))
	for _, e := range p.accounts {
		if !e.account.IsAvailable(now, p.cfg.PrimaryUsedPctMax, p.cfg.SecondaryUsedPctMax) {
			continue
		}
		if exclude != nil && exclude(e.account) {
			continue
		}
		out = append(out, ranked{entry: e, score: score(e.account, e.inflight.Load(), w)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		ii := out[i].entry.inflight.Load()
		jj := out[j].entry.inflight.Load()
		if ii != jj {
			return ii < jj
		}
		return out[i].entry.account.Email < out[j].entry.account.Email
	})
	return out
}
