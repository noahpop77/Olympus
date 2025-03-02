package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/noahpop77/Olympus/endpoints"
	"github.com/noahpop77/Olympus/matchmaking"
	"github.com/noahpop77/Olympus/matchmaking/party"

	"github.com/redis/go-redis/v9"
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

	var partyCancels sync.Map
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	http.HandleFunc("/addMatch", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.InsertIntoDatabase(writer, requester, rdb, ctx)
	})

	http.HandleFunc("/addToQueue", func(writer http.ResponseWriter, requester *http.Request) {
		var unpackedRequest party.Players
		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)
		matchmaking.PartyHandler(writer, &unpackedRequest, rdb, ctx)
	})

	http.HandleFunc("/queueUp", func(writer http.ResponseWriter, requester *http.Request) {
		var unpackedRequest party.Players

		_, cancel := context.WithCancel(context.Background())
		partyCancels.Store(unpackedRequest.PartyId, cancel)

		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)
		matchmaking.PartyHandler(writer, &unpackedRequest, rdb, ctx)
		matchmaking.MatchFinder(writer, &unpackedRequest, rdb, ctx, &partyCancels)
	})

	port := ":8080"
	PrintBanner(port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}

}
