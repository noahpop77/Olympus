package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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
	var matchDataMap sync.Map
	var matchParticipantsMap sync.Map

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// Spawns match in managed sync map to avoid collisions
	http.HandleFunc("/spawnMatch", instrumentedHandler("/addMatch", func(w http.ResponseWriter, r *http.Request) {

		unpackedRequest, err := gameServer.UnpackCreationRequest(w, r)
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

		unpackedRequest, err := gameServer.UnpackConnectionRequest(w, r)
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
					err := gameServer.ConnectPlayerToMatch(&activeMatches, &matchDataMap, match, &matchParticipantsMap, unpackedRequest)
					if err != nil {
						http.Error(w, "Failed to connect player to match", http.StatusInternalServerError)
						return
					}

					// randomMatchData, err := proto.Marshal(randomMatch)
					// if err != nil {
					// 	return
					// }
					// w.Header().Set("Content-Type", "application/x-protobuf")
					// w.Write(randomMatchData)

					// Send a request to a database containing the results of the match
					//

					// TODO: I NEED TO SEND THE REQUESTS TO THE DATABASE IN BATCHES
					// OF 10 SO THAT WHEN ONE LOBBY FINISHES, THE MATCH HISTORY FOR
					// ALL OF THE PLAYERS IN THAT GAME IS UPDATED AT ONCE

					dsn := "postgres://sawa:sawa@postgres:5432/olympus"
					conn, err := pgx.Connect(context.Background(), dsn)
					if err != nil {
						log.Fatalf("Unable to connect to database: %v\n", err)
					}
					defer conn.Close(context.Background())

					value, _ := matchDataMap.Load(match.MatchID)
					var randomMatch *gameServerProto.MatchResult
					if value != nil {
						randomMatch = value.(*gameServerProto.MatchResult)
					}

					participantValue, _ := matchParticipantsMap.Load(match.MatchID)
					var randomParticipants []*gameServerProto.Participant
					if participantValue != nil {
						randomParticipants = participantValue.([]*gameServerProto.Participant)
					}

					participantJsonData, err := json.Marshal(randomParticipants)
					if err != nil {
						log.Fatalf("Failed to convert to JSON: %v", err)
					}

					// Define match data
					matchID := randomMatch.MatchID
					gameVer := randomMatch.GameVersion
					riotID := unpackedRequest.ParticipantPUUID
					gameDuration := randomMatch.GameDuration
					gameCreationTimestamp := randomMatch.GameStartTime
					gameEndTimestamp := randomMatch.GameEndTime
					teamOnePUUID := randomMatch.TeamOnePUUID
					teamTwoPUUID := randomMatch.TeamTwoPUUID
					participants := participantJsonData

					// TODO: Add a function that updates the users profile based off the results of the match
					gameServer.UpdateProfile(conn, unpackedRequest.ParticipantPUUID, unpackedRequest.RiotName, unpackedRequest.RiotTag, randomMatch)
					
					// Execute INSERT query
					_, err = conn.Exec(context.Background(),
						`INSERT INTO "matchHistory" 
						("matchID", "gameVer", "riotID", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants") 
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
						matchID, gameVer, riotID, gameDuration, gameCreationTimestamp, gameEndTimestamp, teamOnePUUID, teamTwoPUUID, participants)

					if err != nil {
						log.Fatalf("Insert failed: %v\n", err)
					}

					w.Header().Set("Content-Type", "application/x-protobuf")
					w.Write([]byte("Database insert performed"))

					return
				}
			}
		}

	}))

	port := ":8081"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
