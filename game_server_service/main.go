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
	var waitGroupMap sync.Map
	var matchCreationDates sync.Map

	var databaseTransactionMutex sync.Mutex

	// go func() {
	// 	for {
	// 		currentTime := time.Now().Unix()

	// 		matchCreationDates.Range(func(key, value interface{}) bool {
	// 			matchCreationDate, ok := value.(int64)
	// 			if !ok {
	// 				return true
	// 			}
	// 			// 86400 is the seconds in a day
	// 			// Every day it checks for for left over artifacts and clears them out.
	// 			// Should be faster for performances sake, lets say an hour or 2
	// 			// Take average game time and multiply it by 2 and use that
	// 			// if currentTime-matchCreationDate > 86400 {
	// 			if currentTime-matchCreationDate > 0 {

	// 				matchCreationDates.Delete(key)
	// 				activeMatches.Delete(key)

	// 			}
	// 			return true
	// 		})

	// 		time.Sleep(15 * time.Second)
	// 		// time.Sleep(5 * time.Minute)
	// 	}
	// }()

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// TODO: Add a timeout and delete to the match created if nobody connects within X amount of time.

	// Matchmaking Service triggers this to spawn a match containing a valid matchID and 10 players that
	// are white listed to connect to that match
	http.HandleFunc("/spawnMatch", instrumentedHandler("/addMatch", func(w http.ResponseWriter, r *http.Request) {

		unpackedRequest, _ := UnpackCreationRequest(w, r)
		activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)
		matchCreationDates.Store(unpackedRequest.MatchID, time.Now().Unix())
	}))

	// Each individual player connects to this endpoint while passing in their PUUID and target matchId
	http.HandleFunc("/connectToMatch", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()

		NewPlayerConnection(w, r, &activeMatches, &matchDataMap, &matchParticipantsMap, &databaseTransactionMutex, &waitGroupMap)
	}))

	// Test function for inspecting sync maps
	http.HandleFunc("/activeMatches", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		var outString string
		matchCreationDates.Range(func(key, value any) bool {
			outString += fmt.Sprintf("\n------------\nmatchCreationDates: %s, %d", key, value)
			return true
		})

		activeMatches.Range(func(key, value any) bool {
			outString += fmt.Sprintf("activeMatches: %s, %s\n", key, value)
			return true
		})

		matchDataMap.Range(func(key, value any) bool {
			outString += fmt.Sprintf("matchDataMap: %s, %s\n", key, value)
			return true
		})

		matchParticipantsMap.Range(func(key, value any) bool {
			outString += fmt.Sprintf("matchParticipantsMap: %s, %s\n", key, value)
			return true
		})

		waitGroupMap.Range(func(key, value any) bool {
			outString += fmt.Sprintf("waitGroupMap: %s, %s\n", key, value)
			return true
		})

		log.Printf("%s", outString)

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
