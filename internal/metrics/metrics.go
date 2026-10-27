// Package metrics registers the operator's Prometheus metrics with the
// controller-runtime registry. Labels never include secret values.
package metrics

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/chiragsoni81245/proton-secrets-operator/internal/proton"
)

var (
	ReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "protonsecret_reconcile_total",
		Help: "Reconciles by result (success, not_found, conflict, auth, transient, error).",
	}, []string{"result"})

	SyncDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "protonsecret_sync_duration_seconds",
		Help:    "Time to resolve references and apply the Secret.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
	})

	LastSync = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "protonsecret_last_sync_timestamp_seconds",
		Help: "Unix time of the last successful sync per ProtonSecret.",
	}, []string{"namespace", "name"})

	PATExpiry = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "protonsecret_pat_expiry_timestamp_seconds",
		Help: "Unix time at which the configured personal access token expires (0 if unknown).",
	})

	PassCLIInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "protonsecret_passcli_info",
		Help: "Constant 1, labelled with the pass-cli version in use.",
	}, []string{"version"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(ReconcileTotal, SyncDuration, LastSync, PATExpiry, PassCLIInfo)
}

// Result maps a reconcile error to a low-cardinality label.
func Result(err error, foreign bool) string {
	switch {
	case err == nil:
		return "success"
	case foreign:
		return "conflict"
	case errors.Is(err, proton.ErrNotFound):
		return "not_found"
	case errors.Is(err, proton.ErrAuth):
		return "auth"
	case errors.Is(err, proton.ErrTransient):
		return "transient"
	default:
		return "error"
	}
}

// SetPATExpiry records the PAT expiry (zero time clears it).
func SetPATExpiry(t time.Time) {
	if t.IsZero() {
		PATExpiry.Set(0)
		return
	}
	PATExpiry.Set(float64(t.Unix()))
}

// WatchPATExpiry returns a Runnable that re-reads an RFC3339 timestamp from
// path every interval and exports it. A missing or invalid file clears the
// metric (0), so a rotated Secret is picked up without a restart. It runs on
// every replica, not only the leader, so the metric is always exported.
func WatchPATExpiry(path string, interval time.Duration) manager.Runnable {
	read := func() {
		b, err := os.ReadFile(path)
		if err != nil {
			SetPATExpiry(time.Time{})
			return
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
		if err != nil {
			SetPATExpiry(time.Time{})
			return
		}
		SetPATExpiry(t)
	}
	return &patWatcher{read: read, interval: interval}
}

type patWatcher struct {
	read     func()
	interval time.Duration
}

func (w *patWatcher) Start(ctx context.Context) error {
	w.read()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.read()
		}
	}
}

// NeedLeaderElection is false: every replica exports the metric.
func (w *patWatcher) NeedLeaderElection() bool { return false }
