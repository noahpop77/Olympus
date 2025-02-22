package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/noahpop77/Olympus/endpoints"
	"github.com/noahpop77/Olympus/matchmaking"
	"github.com/noahpop77/Olympus/matchmaking/party"

	"github.com/redis/go-redis/v9"

	_ "github.com/mattn/go-sqlite3"
)

func PrintBanner(port string) {
	fmt.Println(`================================
▗▖  ▗▖▗▄▄▄▖▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖
▐▛▚▞▜▌  █  ▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘
▐▌  ▐▌  █  ▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ 
▐▌  ▐▌  █  ▐▌ ▐▌▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌`)
	  fmt.Println("================================")
	  fmt.Printf("Starting server on port %s...\n", port)
	  fmt.Println("================================")
}

func main() {
	
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	http.HandleFunc("/addMatch", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.InsertIntoDatabase(writer, requester, rdb, ctx)
	})
	http.HandleFunc("/queueUp", func(writer http.ResponseWriter, requester *http.Request) {
		var unpackedRequest party.Players							// Sets API level state for queueing user
		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)	// Formats and unmarshals sent in data
		matchmaking.PartyHandler(writer, &unpackedRequest, rdb, ctx)	// Adds player to redis DB with relevant info
	})

	http.HandleFunc("/matchmaking", func(writer http.ResponseWriter, requester *http.Request){
		var unpackedRequest party.Players							// Sets API level state for queueing user
		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)	// Formats and unmarshals sent in data
		matchmaking.MatchmakingSelection(writer, &unpackedRequest, rdb, ctx)	// Finds others of similar rank
	})

	port := ":8080"
	PrintBanner(port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}

}
