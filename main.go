package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

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

func IsRunningInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func main() {

	var mu sync.Mutex
	var partyResourcesMap sync.Map
	ctx := context.Background()
	var redisAddr string
	
	if IsRunningInDocker() {
		redisAddr = "redis_db:6379" // Docker service name
	} else {
		redisAddr = "localhost:6379" // Local development
	}

	// For the Addr, set it to localhost for locally deployed Redis and container name for containrerized version
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	http.HandleFunc("/addToQueue", func(writer http.ResponseWriter, requester *http.Request) {
		var unpackedRequest party.Players
		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)
		matchmaking.AddPartyToRedis(writer, &unpackedRequest, rdb, ctx)
	})

	http.HandleFunc("/queueUp", func(writer http.ResponseWriter, requester *http.Request) {
		var unpackedRequest party.Players
		matchmaking.UnpackRequest(writer, requester, &unpackedRequest)

		matchmakingContext, cancel := context.WithCancel(context.Background())
		partyResourcesMap.Store(unpackedRequest.PartyId, matchmaking.PartyResources{
			CancelFunc: cancel,
			Writer:     writer,
		})

		matchmaking.AddPartyToRedis(writer, &unpackedRequest, rdb, ctx)
		matchmaking.MatchFinder(writer, &unpackedRequest, rdb, ctx, &partyResourcesMap, matchmakingContext, requester, &mu)
	})

	port := ":8080"
	PrintBanner(port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}

}
