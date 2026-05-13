package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/FL-Penly/proxy-gate/ingress"
	"github.com/FL-Penly/proxy-gate/store"
)

func TestAdminClaudeFallbackPutPersistsAndUpdatesRuntime(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := NewQueue(st)
	defer q.Close()
	runtime := ingress.NewClaudeFallbackRuntime(ingress.DefaultClaudeFallbackPolicy())
	api := &AdminAPI{Token: "tok", Queue: q, ClaudeFallback: runtime}

	body := map[string]any{"policy": ingress.ClaudeFallbackPolicy{
		Enabled: true,
		Rules: []ingress.ClaudeFallbackRule{
			{Enabled: true, FromModel: "claude-haiku4.5", ToModel: "claude-sonnet4.6", ToVariant: "thinking-16k"},
		},
	}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/admin/claude-fallback", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	api.putClaudeFallback(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := runtime.Policy().Rules[0].ToModel; got != "claude-sonnet-4-6" {
		t.Fatalf("runtime model=%q", got)
	}
	q.Flush()
	if _, err := st.Get(store.BucketSettings, ClaudeFallbackSettingsKey); err != nil {
		t.Fatalf("policy not persisted: %v", err)
	}
}
