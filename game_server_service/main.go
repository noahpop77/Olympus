package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "game_server_api_requests_total",
			Help: "Total number of requests to API endpoints",
		},
		[]string{"endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "game_server_api_request_duration_seconds",
			Help:    "Histogram of response time for API requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "game_server_queueup_active_players",
			Help: "Number of players currently connected to /connectToMatch",
		},
	)
)

// Middleware for Prometheus metrics
func instrumentedHandler(endpoint string, handler func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		handler(w, r)
		duration := time.Since(start).Seconds()

		requestsTotal.WithLabelValues(endpoint).Inc()
		requestDuration.WithLabelValues(endpoint).Observe(duration)
	}
}

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, activeConnections)
}

func main() {
	var activeMatches sync.Map
	var matchDataMap sync.Map
	var matchParticipantsMap sync.Map
	var databaseTransactionMutex sync.Mutex

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// TODO: Add a timeout and delete to the match created if nobody connects within X amount of time.

	// Spawns match in managed sync map to avoid collisions
	http.HandleFunc("/spawnMatch", instrumentedHandler("/addMatch", func(w http.ResponseWriter, r *http.Request) {

		unpackedRequest, err := UnpackCreationRequest(w, r)
		if err != nil {
			http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
			return
		}
		activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)

	}))

	http.HandleFunc("/connectToMatch", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()
		NewPlayerConnection(w, r, &activeMatches, &matchDataMap, &matchParticipantsMap, &databaseTransactionMutex)

	}))

	port := ":8081"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
