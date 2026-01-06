package ipc

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/odvcencio/buckley/pkg/storage"
)

var (
	metricActiveSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "buckley",
		Name:      "sessions_active_total",
		Help:      "Number of active Buckley workflow sessions.",
	})
	metricAuthSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "buckley",
		Name:      "web_sessions_total",
		Help:      "Number of authenticated web sessions.",
	})
	metricCliTickets = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "buckley",
		Name:      "cli_tickets_pending_total",
		Help:      "Pending CLI login tickets awaiting approval.",
	})
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authorize(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	// Check if scope is valid and at least viewer level
	rank, known := scopeRank[strings.ToLower(principal.Scope)]
	requiredRank := scopeRank[strings.ToLower(storage.TokenScopeViewer)]
	if !known || rank < requiredRank {
		respondError(w, http.StatusForbidden, errors.New("forbidden"))
		return
	}
	promhttp.Handler().ServeHTTP(w, r)
}

func (s *Server) refreshTicketGauge() {
	if s.store == nil {
		return
	}
	if count, err := s.store.CountPendingCLITickets(time.Now()); err == nil {
		metricCliTickets.Set(float64(count))
	}
}

func (s *Server) refreshAuthSessionGauge() {
	if s.store == nil {
		return
	}
	if count, err := s.store.CountActiveAuthSessions(time.Now()); err == nil {
		metricAuthSessions.Set(float64(count))
	}
}
