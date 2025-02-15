package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/noahpop77/Olympus/endpoints"

	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

var sqliteDB *sql.DB

func initDB() {
	var err error
	sqliteDB, err = sql.Open("sqlite3", "mtrack.db")
	fmt.Println("Connection to sqlite open")
	if err != nil {
		log.Fatal(err)
	}

	_, err = sqliteDB.Exec("PRAGMA journal_mode = WAL;")
	if err != nil {
		log.Fatalf("Error setting WAL mode: %v", err)
	}

	_, err = sqliteDB.Exec("PRAGMA busy_timeout = 5000;") // Wait for up to 5 seconds
	if err != nil {
		log.Fatalf("Error setting busy timeout: %v", err)
	}

	fmt.Println("Creating matchHistory")
	sqliteDB.Exec(`CREATE TABLE "matchHistory" (
		"gameID"                VARCHAR(16) NOT NULL,
		"gameVer"               VARCHAR(16) NOT NULL,
		"riotID"                VARCHAR(45) NOT NULL,
		"gameDurationMinutes"   VARCHAR(16) NOT NULL,
		"gameCreationTimestamp" VARCHAR(16) NOT NULL,
		"gameEndTimestamp"      VARCHAR(16) NOT NULL,
		"queueType"             VARCHAR(45) NOT NULL,
		"gameDate"              VARCHAR(45) NOT NULL,
		"participants"          JSON NOT NULL,
		"matchData"             JSON NOT NULL,
		CONSTRAINT unique_pair_index UNIQUE ("gameID", "riotID")
	);`)
	fmt.Println("Created matchHistory")

	fmt.Println("Creating riotIDData")
	sqliteDB.Exec(`CREATE TABLE "riotIDData" (
		"riotID" VARCHAR(25) NOT NULL,
		"puuid"  VARCHAR(100) NOT NULL,
		PRIMARY KEY ("riotID")
	);`)
	fmt.Println("Created riotIDData")

	fmt.Println("Creating summonerRankedInfo")
	sqliteDB.Exec(`CREATE TABLE "summonerRankedInfo" (
		"encryptedPUUID" VARCHAR(100) NOT NULL,
		"summonerID"     VARCHAR(100) NOT NULL,
		"riotID"         VARCHAR(45) NOT NULL,
		"tier"           VARCHAR(45) NOT NULL,
		"rank"           VARCHAR(45) NOT NULL,
		"leaguePoints"   VARCHAR(45) NOT NULL,
		"queueType"      VARCHAR(45) NOT NULL,
		"wins"           VARCHAR(45) NOT NULL,
		"losses"         VARCHAR(45) NOT NULL,
		PRIMARY KEY ("encryptedPUUID")
	);`)
	fmt.Println("Created summonerRankedInfo")

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Connected to the database successfully.")
}

func main() {
	// Initialize the database connection
	initDB()
	defer sqliteDB.Close()

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
