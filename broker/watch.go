package broker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

func (p *Pool) WatchDir(ctx context.Context, dir string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return err
	}
	go p.runWatcher(ctx, w, dir, logger)
	return nil
}

func (p *Pool) runWatcher(ctx context.Context, w *fsnotify.Watcher, dir string, logger *slog.Logger) {
	defer w.Close()
	debounce := make(map[string]*time.Timer)
	const wait = 200 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			for _, t := range debounce {
				t.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".json") {
				continue
			}
			path := ev.Name
			if t, ok := debounce[path]; ok {
				t.Stop()
			}
			debounce[path] = time.AfterFunc(wait, func() {
				p.handleFileEvent(ev.Op, path, logger)
			})
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			logger.Warn("watcher error", "err", err, "dir", dir)
		}
	}
}

func (p *Pool) handleFileEvent(op fsnotify.Op, path string, logger *slog.Logger) {
	if op&fsnotify.Remove != 0 || op&fsnotify.Rename != 0 {
		if email := emailBySource(p, path); email != "" {
			p.Remove(email)
			logger.Info("account removed", "email", email, "path", path)
		}
		return
	}
	acc, err := LoadAccountFile(path)
	if err != nil {
		logger.Warn("account file invalid", "err", err, "path", path)
		return
	}
	p.Add(acc)
	logger.Info("account loaded", "email", acc.Email, "path", path)
}

func emailBySource(p *Pool, path string) string {
	for _, a := range p.List() {
		if filepath.Clean(a.SourcePath()) == filepath.Clean(path) {
			return a.Email
		}
	}
	return ""
}

var _ = strings.TrimSuffix
