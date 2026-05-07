package provider

import (
	"time"

	"github.com/tidwall/gjson"
)

func parseWhamUsage(body []byte) (WhamUsage, error) {
	root := gjson.ParseBytes(body)
	primary := root.Get("rate_limit.primary_window")
	secondary := root.Get("rate_limit.secondary_window")
	return WhamUsage{
		PrimaryUsedPct:   primary.Get("used_percent").Float() / 100.0,
		SecondaryUsedPct: secondary.Get("used_percent").Float() / 100.0,
		PrimaryResetAt:   resetAtFrom(primary),
		SecondaryResetAt: resetAtFrom(secondary),
		LimitReached:     root.Get("rate_limit.limit_reached").Bool(),
		PlanType:         root.Get("plan_type").String(),
	}, nil
}

func resetAtFrom(window gjson.Result) time.Time {
	if epoch := window.Get("reset_at").Int(); epoch > 0 {
		return time.Unix(epoch, 0)
	}
	if after := window.Get("reset_after_seconds").Int(); after > 0 {
		return time.Now().Add(time.Duration(after) * time.Second)
	}
	return time.Time{}
}
