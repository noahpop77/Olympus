package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/noahpop77/Olympus/platform_service/platformProto"
)

func main() {
	fmt.Println("Platform Service")

	http.HandleFunc("/matchHistory", func(w http.ResponseWriter, r *http.Request) {
		var unpackedRequest platformProto.MatchHistory
		UnpackRequest(w, r, &unpackedRequest)

		dsn := "postgres://sawa:sawa@postgres:5432/olympus"
		conn, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			log.Fatalf("Unable to connect to database: %v\n", err)
		}
		defer conn.Close(context.Background())

		var rank, wins, losses int
		err = conn.QueryRow(context.Background(),
			`SELECT * FROM "matchHistory" WHERE puuid = $1`, unpackedRequest.PlayerPUUID).
			Scan(&rank, &wins, &losses)
		if err == pgx.ErrNoRows {
			// Row doesn't exist
		} else if err != nil && err != pgx.ErrNoRows {
			log.Fatal("Failed to fetch match history from DB: %v\n", err)
		}
	})
}
