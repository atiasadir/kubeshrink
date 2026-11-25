// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package serve

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/live"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// Config configures a Server.
type Config struct {
	Listen     string
	Interval   time.Duration
	Timeout    time.Duration
	PoolLabels []string
	Options    explain.Options
	// Load reads the cluster. It must only list.
	Load     func(ctx context.Context) (*snapshot.Snapshot, error)
	Logger   *slog.Logger
	Version  string
	Revision string
}

// Server scans the cluster on an interval and serves what it found.
type Server struct {
	cfg     Config
	metrics *Metrics
	ready   atomic.Bool
}

// New prepares a Server; Run starts it.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if len(cfg.PoolLabels) == 0 {
		cfg.PoolLabels = DefaultPoolLabels
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Minute
	}
	return &Server{cfg: cfg, metrics: NewMetrics(cfg.Version, cfg.Revision)}
}

// Handler serves /metrics, /healthz (the process is up) and /readyz (a scan
// has succeeded).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "no successful scan yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

// Scan reads the cluster once, analyzes it and replaces the exported values.
func (s *Server) Scan(ctx context.Context) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	snap, err := s.cfg.Load(ctx)
	if err != nil {
		resource := "unknown"
		var le *live.ListError
		if errors.As(err, &le) {
			resource = le.Resource
		}
		s.metrics.scanErrors.WithLabelValues(resource).Inc()
		s.metrics.scanDuration.Set(time.Since(start).Seconds())
		s.cfg.Logger.Error("scan failed", "resource", resource, "error", err.Error(), "duration_seconds", time.Since(start).Seconds())
		return err
	}
	for _, r := range snap.Unlisted {
		s.metrics.scanErrors.WithLabelValues(r).Inc()
	}
	o := observe(snap, explain.NewAnalyzer(snap, s.cfg.Options), s.cfg.PoolLabels)
	s.metrics.scan.set(o)
	dur := time.Since(start).Seconds()
	s.metrics.scanDuration.Set(dur)
	s.metrics.lastSuccess.SetToCurrentTime()
	s.ready.Store(true)
	blocked := 0.0
	for k, v := range o.nodes {
		if k[0] == explain.VerdictBlocked {
			blocked += v
		}
	}
	s.cfg.Logger.Info("scan complete", "duration_seconds", dur, "nodes", len(snap.Nodes), "pods", len(snap.Pods),
		"nodes_needed", o.needed["requests"], "blocked_nodes", blocked, "unlisted", snap.Unlisted)
	return nil
}

// Run scans now and then every Interval, serving Handler on Listen, until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.cfg.Logger.Info("serving", "listen", s.cfg.Listen, "interval", s.cfg.Interval.String(), "version", s.cfg.Version)
	_ = s.Scan(ctx)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(sctx)
		case err := <-errc:
			return err
		case <-t.C:
			_ = s.Scan(ctx)
		}
	}
}
