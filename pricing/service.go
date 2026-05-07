package pricing

import (
	"context"
	"time"
)

type Service struct {
	Source  *Source
	Fetcher *Fetcher
}

type StatusReport struct {
	Origin      string    `json:"origin"`
	ModelsCount int       `json:"models_count"`
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
	URL         string    `json:"url,omitempty"`
	LastAttempt time.Time `json:"last_attempt_at,omitzero"`
	LastSuccess time.Time `json:"last_success_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
}

func (s *Service) Status() StatusReport {
	snap := s.Source.Snapshot()
	report := StatusReport{
		Origin:      snap.Origin,
		ModelsCount: len(snap.Models),
		FetchedAt:   snap.FetchedAt,
	}
	if s.Fetcher != nil {
		fs := s.Fetcher.Status()
		report.URL = fs.URL
		report.LastAttempt = fs.LastAttempt
		report.LastSuccess = fs.LastSuccess
		report.LastError = fs.LastError
	}
	return report
}

func (s *Service) Refresh(ctx context.Context) error {
	if s.Fetcher == nil {
		return nil
	}
	return s.Fetcher.Refresh(ctx)
}

func (s *Service) Misses() map[string]int64 {
	return s.Source.Misses()
}
