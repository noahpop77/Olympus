package main

import (
	"context"
	"fmt"
	"log"
	"m-track-go/endpoints"
	"net/http"

	"github.com/jackc/pgx/v4/pgxpool"
)

var dbpool *pgxpool.Pool

// Initialize the database connection pool
func initDB() {
	var err error
	dbpool, err = pgxpool.Connect(context.Background(), "postgres://sawa:sawa@localhost:5432/mtrack")
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	fmt.Println("Connected to the database successfully.")
}

func main() {
	// Initialize the database connection
	initDB()

	// Set up your handlers
	http.HandleFunc("/printJson", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.PrintJsonHandler(writer, requester)
	})

	http.HandleFunc("/addMatch", func(writer http.ResponseWriter, requester *http.Request) {
		endpoints.InsertIntoDatabase(writer, requester, dbpool)
	})

	fmt.Println(`
  ▗▖  ▗▖▗▄▄▄▖▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖
  ▐▛▚▞▜▌  █  ▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘
  ▐▌  ▐▌  █  ▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ 
  ▐▌  ▐▌  █  ▐▌ ▐▌▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌
===================================`)

	// Start the server
	port := ":80"
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("===================================")
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Error starting server: %v\n", err)
	}
}