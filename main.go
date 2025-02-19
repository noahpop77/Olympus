package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/noahpop77/Olympus/endpoints"
	"github.com/noahpop77/Olympus/matchmaking"

	"github.com/redis/go-redis/v9"

	_ "github.com/mattn/go-sqlite3"
)

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
	http.HandleFunc("/matchmaking", func(writer http.ResponseWriter, requester *http.Request) {
		matchmaking.PartyHandler(writer, requester, rdb, ctx)
	})

	fmt.Println(`
  ▗▖  ▗▖▗▄▄▄▖▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖
  ▐▛▚▞▜▌  █  ▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘
  ▐▌  ▐▌  █  ▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ 
  ▐▌  ▐▌  █  ▐▌ ▐▌▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌`)

	// Start the server
	port := ":8080"
	fmt.Println("===================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("===================================")

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}

}
