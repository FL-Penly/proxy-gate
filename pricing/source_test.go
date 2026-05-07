package pricing

import (
	"testing"
)

func TestEmbeddedSnapshotLoads(t *testing.T) {
	snap, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if snap.Origin != OriginEmbedded {
		t.Errorf("origin=%q want %q", snap.Origin, OriginEmbedded)
	}
	if len(snap.Models) < 50 {
		t.Errorf("expected at least 50 OpenAI models, got %d", len(snap.Models))
	}
	for _, m := range []string{"gpt-5", "gpt-5-codex", "gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.5-pro"} {
		if _, ok := snap.Models[m]; !ok {
			t.Errorf("expected %q in embedded snapshot", m)
		}
	}
}

func TestLookupDirect(t *testing.T) {
	src := newSourceWithFixtures()
	p, ok := src.Lookup("gpt-5.4")
	if !ok {
		t.Fatal("expected gpt-5.4 to be found")
	}
	if p.InputCost == 0 {
		t.Error("gpt-5.4 input cost should be non-zero")
	}
}

func TestLookupCodexAlias(t *testing.T) {
	models := map[string]CompactPrice{
		"gpt-5": {InputCost: 1.25e-6, OutputCost: 1e-5, CacheReadCost: 1.25e-7},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})
	p, ok := src.Lookup("gpt-5-codex")
	if !ok {
		t.Fatalf("alias resolution failed: gpt-5-codex should fall back to gpt-5")
	}
	if p.InputCost != 1.25e-6 {
		t.Errorf("alias resolved to wrong entry: %+v", p)
	}
}

func TestLookupAliasOnlyWhenDirectMissing(t *testing.T) {
	models := map[string]CompactPrice{
		"gpt-5":         {InputCost: 1e-6, OutputCost: 5e-6},
		"gpt-5-codex":   {InputCost: 2e-6, OutputCost: 8e-6, CacheReadCost: 1e-7},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})
	p, ok := src.Lookup("gpt-5-codex")
	if !ok {
		t.Fatal("not found")
	}
	if p.InputCost != 2e-6 {
		t.Errorf("direct match must win over alias: got %v", p.InputCost)
	}
}

func TestLookupOpenAIPrefixStrip(t *testing.T) {
	models := map[string]CompactPrice{
		"gpt-5.4": {InputCost: 2.5e-6, OutputCost: 1.5e-5, CacheReadCost: 2.5e-7},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})
	p, ok := src.Lookup("openai/gpt-5.4")
	if !ok {
		t.Fatal("openai/ prefix should be stripped to find gpt-5.4")
	}
	if p.InputCost != 2.5e-6 {
		t.Errorf("wrong entry: %+v", p)
	}
}

func TestLookupReturnsFalseForUnknown(t *testing.T) {
	src := newSourceWithFixtures()
	if _, ok := src.Lookup("totally-fake-model-xyz"); ok {
		t.Error("expected miss for unknown model")
	}
	if _, ok := src.Lookup(""); ok {
		t.Error("empty model must miss")
	}
	if _, ok := src.Lookup("unknown"); ok {
		t.Error("literal 'unknown' must miss")
	}
}

func TestLookupSkipsZeroPricedEntries(t *testing.T) {
	models := map[string]CompactPrice{
		"chatgpt/gpt-5.4": {},
		"gpt-5.4":         {InputCost: 2.5e-6, OutputCost: 1.5e-5, CacheReadCost: 2.5e-7},
	}
	src := NewSource(&Snapshot{Models: models, Origin: OriginEmbedded})
	if _, ok := src.Lookup("chatgpt/gpt-5.4"); ok {
		t.Error("zero-priced chatgpt/ entry should be considered a miss")
	}
}

func TestRecordMisses(t *testing.T) {
	src := newSourceWithFixtures()
	src.RecordMiss("gpt-99")
	src.RecordMiss("gpt-99")
	src.RecordMiss("gpt-100")
	m := src.Misses()
	if m["gpt-99"] != 2 {
		t.Errorf("gpt-99 count=%d", m["gpt-99"])
	}
	if m["gpt-100"] != 1 {
		t.Errorf("gpt-100 count=%d", m["gpt-100"])
	}
}

func TestBillingKeyComposition(t *testing.T) {
	cases := []struct {
		model, tier, want string
	}{
		{"gpt-5.4", "", "gpt-5.4"},
		{"gpt-5.4", "default", "gpt-5.4"},
		{"gpt-5.4", "auto", "gpt-5.4"},
		{"gpt-5.4", "priority", "gpt-5.4@priority"},
		{"gpt-5.4", "PRIORITY", "gpt-5.4@priority"},
		{"gpt-5.4", "flex", "gpt-5.4@flex"},
		{"gpt-5.4-mini", "priority", "gpt-5.4-mini@priority"},
		{"", "priority", ""},
	}
	for _, c := range cases {
		got := BillingKey(c.model, c.tier)
		if got != c.want {
			t.Errorf("BillingKey(%q,%q)=%q want %q", c.model, c.tier, got, c.want)
		}
	}
}

func TestReplaceRejectsEmptySnapshot(t *testing.T) {
	src := newSourceWithFixtures()
	before := len(src.Snapshot().Models)
	src.Replace(nil)
	src.Replace(&Snapshot{Models: map[string]CompactPrice{}})
	after := len(src.Snapshot().Models)
	if before != after {
		t.Errorf("empty replace altered snapshot: %d → %d", before, after)
	}
}

func TestReplaceSetsFetchedAt(t *testing.T) {
	src := NewSource(nil)
	src.Replace(&Snapshot{
		Models: map[string]CompactPrice{"gpt-5": {InputCost: 1e-6}},
		Origin: OriginLiteLLM,
	})
	if src.Snapshot().FetchedAt.IsZero() {
		t.Error("Replace should backfill FetchedAt when zero")
	}
}

func newSourceWithFixtures() *Source {
	snap, err := LoadEmbedded()
	if err != nil {
		panic(err)
	}
	return NewSource(snap)
}
