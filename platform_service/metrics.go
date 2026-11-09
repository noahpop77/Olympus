package main

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "match_history_api_requests_total",
			Help: "Total number of requests to API endpoints",
		},
		[]string{"endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "match_history_request_duration_seconds",
			Help:    "Histogram of response time for API requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)
)

// Middleware for Prometheus metrics
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

		// The actual api request and functionality is here and everything else
		// in the function is middleware fancieness
		handler(w, r)
	}
}

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration)
}
