package main

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Define Prometheus metrics
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "matchmaking_api_requests_total",
			Help: "Total number of requests to API endpoints",
		},
		[]string{"endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "matchmaking_api_request_duration_seconds",
			Help:    "Histogram of response time for API requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "matchmaking_queueup_active_players",
			Help: "Number of players currently connected to /queueUp",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, activeConnections)
}

func instrumentedHandler(endpoint string, handler func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// A panic in one handler must not take down the whole service. net/http
		// already recovers per-connection, but that silently drops the client;
		// logging here makes the failure visible.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in %s: %v\n%s", endpoint, rec, debug.Stack())
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
			duration := time.Since(start).Seconds()
			requestsTotal.WithLabelValues(endpoint).Inc()
			requestDuration.WithLabelValues(endpoint).Observe(duration)
		}()

		handler(w, r)
	}
}
