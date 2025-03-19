package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	gameServer "github.com/noahpop77/Olympus/game_server_service/game_server"
	"github.com/noahpop77/Olympus/game_server_service/game_server/gameServerProto"

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

func PrintBanner(port string) {
	fmt.Println(`=================================================
 ██████   █████  ███    ███ ███████ 
██       ██   ██ ████  ████ ██      
██   ███ ███████ ██ ████ ██ █████ 
██    ██ ██   ██ ██  ██  ██ ██    
 ██████  ██   ██ ██      ██ ███████ 
                                    
███████ ███████ ██████  ██    ██ ███████ ██████ 
██      ██      ██   ██ ██    ██ ██      ██   ██ 
███████ █████   ██████  ██    ██ █████   ██████  
     ██ ██      ██   ██  ██  ██  ██      ██   ██ 
███████ ███████ ██   ██   ████   ███████ ██   ██ `)
	fmt.Println("=================================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("=================================================")
}

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, activeConnections)
}

func main() {
	// var mu sync.Mutex
	var activeMatches sync.Map

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// Spawns match in managed sync map to avoid collisions
	http.HandleFunc("/spawnMatch", instrumentedHandler("/addMatch", func(w http.ResponseWriter, r *http.Request) {

		unpackedRequest, err := gameServer.UnpackCreationRequest(w,r)
		if err != nil {
			http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
			return
		}

		activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)
	}))

	// Endpoint that users will use to connect to the marked matches in the sync.Map
	http.HandleFunc("/connectToMatch", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		//Tracks people currently in game using the game_server_queueup_active_players datapoint
		activeConnections.Inc()
		defer activeConnections.Dec()

		unpackedRequest, err := gameServer.UnpackConnectionRequest(w,r)
		if err != nil {
			http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
			return
		}

		// Loads data for relevant match that is in the marked sync.Map
		validateSyncStore, ok := activeMatches.Load(unpackedRequest.MatchID)
		if ok {
			match, valid := validateSyncStore.(*gameServerProto.MatchCreation)
			if !valid {
				fmt.Println("Error: Type assertion failed")
				return
			}
			
			// Loops through match PUUIDs in requested match ID to find out if you are in it
			for _, value := range match.ParticipantsPUUID {
				if value == unpackedRequest.ParticipantPUUID {
					data, err := gameServer.ConnectPlayerToMatch(&activeMatches, match)
					if err != nil {
						http.Error(w, "Failed to connect player to match", http.StatusInternalServerError)
						return
					}
					w.Header().Set("Content-Type", "application/x-protobuf")
					w.Write(data)

					// Send a request to a database containing the results of the match
					// 

					return
				}
			}
		}

	}))

	port := ":8081"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}