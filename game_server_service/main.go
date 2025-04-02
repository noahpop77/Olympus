package main

import (
	"fmt"
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
	// Global data structures containing global match info so different requests
	//     can share data, other options are probably best but this works
	var activeMatches sync.Map
	var matchDataMap sync.Map
	var matchParticipantsMap sync.Map
	var databaseTransactionMutex sync.Mutex
	var waitGroupMap sync.Map

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// TODO: Add a timeout and delete to the match created if nobody connects within X amount of time.

	// Matchmaking Service triggers this to spawn a match containing a valid matchID and 10 players that
	// are white listed to connect to that match
	http.HandleFunc("/spawnMatch", instrumentedHandler("/spawnMatch", func(w http.ResponseWriter, r *http.Request) {

		unpackedRequest, err := UnpackCreationRequest(w, r)
		if err != nil {
			http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
			return
		}
		activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)

	}))

	// Each individual player connects to this endpoint while passing in their PUUID and target matchId
	http.HandleFunc("/connectToMatch", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()

		NewPlayerConnection(w, r, &activeMatches, &matchDataMap, &matchParticipantsMap, &databaseTransactionMutex, &waitGroupMap)
	}))

	// Test function for inspecting sync maps
	http.HandleFunc("/activeMatches", instrumentedHandler("/activeMatches", func(w http.ResponseWriter, r *http.Request) {
		var outString string
		matchDataMap.Range(func(key, value interface{}) bool {
			outString += fmt.Sprintf("%s, %s\n", key, value)
			return true
		})

		waitGroupMap.Range(func(key, value interface{}) bool {
			outString += fmt.Sprintf("%s, %s\n", key, value)
			return true
		})

		w.Write([]byte(outString))
	}))

	http.HandleFunc("/health", instrumentedHandler("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Launch web server
	port := ":8081"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
