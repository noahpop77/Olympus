package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

/*

/metrics & /health - Are Prometheus specific endpoints used for scraping developer specified performance
metrics and visualizing it in Grafana

/spawnMatch - Matchmaking Service triggers this to spawn a match containing a valid matchID and 10 players that are white listed to connect to that match

/connectToMatch - Each individual player connects to this endpoint while passing in their PUUID and target matchId
*/

func main() {
	// Global data structures containing global match info so different requests
	//     can share data, other options are probably best but this works
	var activeMatches sync.Map
	var matchDataMap sync.Map
	var matchParticipantsMap sync.Map
	var waitGroupMap sync.Map
	var matchCreationDates sync.Map

	var databaseTransactionMutex sync.Mutex

	initDB()

	matchHeartBeat(&activeMatches, &matchCreationDates)

	// Endpoint used to expose prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	
	http.HandleFunc("/spawnMatch", instrumentedHandler("/spawnMatch", func(w http.ResponseWriter, r *http.Request) {
		unpackedRequest, _ := UnpackCreationRequest(w, r)
		activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)
		matchCreationDates.Store(unpackedRequest.MatchID, time.Now().Unix())
	}))

	http.HandleFunc("/connectToMatch", instrumentedHandler("/connectToMatch", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()

		NewPlayerConnection(w, r, &activeMatches, &matchDataMap, &matchParticipantsMap, &databaseTransactionMutex, &waitGroupMap)
	}))
	

	// DEBUG PURPOSES ONLY: Test function for inspecting sync maps
	http.HandleFunc("/activeMatches", instrumentedHandler("/activeMatches", func(w http.ResponseWriter, r *http.Request) {
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


	// Launch web server
	port := ":8081"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
