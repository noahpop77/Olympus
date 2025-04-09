package main

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

/*
/metrics & /health - Simulates looking at the match history page of the client. Returns a list of matches with their match ID values as well as their in game data

/riotProfile - Simulates looking at your summoner profile. Returns puuid, riotName, riotTag, rank, wins, losses
*/

func main() {
	initDB()
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	
	
	http.HandleFunc("/databaseHealth", func(w http.ResponseWriter, r *http.Request) {
		DatabaseHealthCheck(w, r)
	})
	
	http.HandleFunc("/matchHistory", instrumentedHandler("/matchHistory", func(w http.ResponseWriter, r *http.Request) {
		GetMatchHistory(w, r)
	}))

	http.HandleFunc("/riotProfile", instrumentedHandler("/riotProfile", func(w http.ResponseWriter, r *http.Request) {
		RiotProfile(w, r)
	}))


	port := ":8082"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
