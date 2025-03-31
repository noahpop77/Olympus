package main

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

		// The actual api request and functionality is here and everything else
		// in the function is middleware fancieness
		handler(w, r)

		duration := time.Since(start).Seconds()

		requestsTotal.WithLabelValues(endpoint).Inc()
		requestDuration.WithLabelValues(endpoint).Observe(duration)
	}
}

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration)
}

func main() {
	http.Handle("/metrics", promhttp.Handler())

	// Simulates looking at the match history page of the client
	// Returns a list of matches with their match ID values as well as their in game data
	http.HandleFunc("/matchHistory", instrumentedHandler("/matchHistory", func(w http.ResponseWriter, r *http.Request) {
		GetMatchHistory(w, r)
	}))

	// Simulates looking at your summoner profile
	// Returns puuid, riotName, riotTag, rank, wins, losses
	http.HandleFunc("/riotProfile", instrumentedHandler("/matchHistory", func(w http.ResponseWriter, r *http.Request) {
		RiotProfile(w, r)
	}))

	port := ":8082"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
