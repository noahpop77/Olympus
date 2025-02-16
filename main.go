package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"context"
	"github.com/noahpop77/Olympus/endpoints"
	"github.com/redis/go-redis"
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

var sqliteDB *sql.DB

func initDB() {

	ctx := context.Background()
	fmt.Println(ctx)
	rdb := redis.
	
	fmt.Println("Creating matchHistory")

	fmt.Println("Created matchHistory")

	fmt.Println("Creating riotIDData")

	fmt.Println("Created riotIDData")

	fmt.Println("Creating summonerRankedInfo")

	fmt.Println("Created summonerRankedInfo")

	fmt.Println("Connected to the database successfully.")
}

func main() {
	// Initialize the database connection
	initDB()
	defer sqliteDB.Close()

	dir, err := os.Getwd()
    if err != nil {
        fmt.Println("Error:", err)
        return
    }

    fmt.Println("Current directory:", dir)

	// Set up your handlers
	http.HandleFunc("/printJson", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.PrintJsonHandler(writer, requester)
	})

	http.HandleFunc("/addMatch", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.InsertIntoDatabase(writer, requester, sqliteDB)
	})

	fmt.Println(`
  ▗▖  ▗▖▗▄▄▄▖▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖
  ▐▛▚▞▜▌  █  ▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘
  ▐▌  ▐▌  █  ▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ 
  ▐▌  ▐▌  █  ▐▌ ▐▌▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌
===================================`)

	// Start the server
	port := ":8080"
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("===================================")

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}

}
