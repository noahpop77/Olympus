package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noahpop77/Olympus/platform_service/platformProto"
	"google.golang.org/protobuf/proto"
)

const (
	// dbOpTimeout bounds every database round trip so a slow or saturated
	// Postgres cannot pin request goroutines indefinitely.
	dbOpTimeout = 5 * time.Second

	// matchHistoryLimit caps how many matches /matchHistory returns. Without
	// a limit every call returns the player's entire history, which grows
	// linearly for the whole run: after ~25 minutes each of 10k players has
	// ~150 matches (~1 MB of rows), and 10k concurrent calls read several GB
	// per wave and time out. Real match history is paginated; 20 is a sane
	// first page and keeps each response around 100-150 KB.
	matchHistoryLimit = 20
)

func PrintBanner(port string) {
	fmt.Println(`=====================================================================
██████╗ ██╗      █████╗ ████████╗███████╗ ██████╗ ██████╗ ███╗   ███╗
██╔══██╗██║     ██╔══██╗╚══██╔══╝██╔════╝██╔═══██╗██╔══██╗████╗ ████║
██████╔╝██║     ███████║   ██║   █████╗  ██║   ██║██████╔╝██╔████╔██║
██╔═══╝ ██║     ██╔══██║   ██║   ██╔══╝  ██║   ██║██╔══██╗██║╚██╔╝██║
██║     ███████╗██║  ██║   ██║   ██║     ╚██████╔╝██║  ██║██║ ╚═╝ ██║
╚═╝     ╚══════╝╚═╝  ╚═╝   ╚═╝   ╚═╝      ╚═════╝ ╚═╝  ╚═╝╚═╝     ╚═╝`)
	fmt.Println("=====================================================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("=====================================================================")
}

// Base function for forms of unpacking requests
func UnpackRequest(w http.ResponseWriter, r *http.Request, protoMessage proto.Message) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("invalid method and expecting POST but got: %v", r.Method)
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("error reading body from %s: %v", r.URL.Path, err)
	}

	// Unmarshal into the provided proto message type
	err = proto.Unmarshal(data, protoMessage)
	if err != nil {
		return fmt.Errorf("error unmarshalling data from %s: %v", r.URL.Path, err)
	}

	return nil
}

var dbPool *pgxpool.Pool

func initDB() {
	// Keep the pool well below Postgres max_connections (see dbScripts/init.sql
	// and docker-compose.yaml). The old value of 10000 meant pgxpool would try
	// to open far more connections than Postgres would ever accept.
	dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=50&pool_min_conns=5&pool_max_conn_idle_time=1m&pool_max_conn_lifetime=30m"

	var err error
	dbPool, err = pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Failed to connect to DB pool: %v", err)
	}
}

func DatabaseHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dbOpTimeout)
	defer cancel()

	var result string
	err := dbPool.QueryRow(ctx, "SELECT 'ok'").Scan(&result)
	if err != nil {
		http.Error(w, "Database query failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// Gets some riot account stats as specified in the summonerRankedInfo table of the postgres database
func RiotProfile(w http.ResponseWriter, r *http.Request) {
	var unpackedRequest platformProto.UserProfile
	if err := UnpackRequest(w, r, &unpackedRequest); err != nil {
		log.Printf("Could not unpack the payload: %s\n", err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbOpTimeout)
	defer cancel()

	// Gets the single row containing the account information, nothing else
	rows, err := dbPool.Query(ctx,
		`SELECT puuid, "riotName", "riotTag", rank, wins, losses FROM "summonerRankedInfo" WHERE "puuid" = $1`, unpackedRequest.Puuid)
	if err != nil {
		log.Printf("Failed to fetch summoner ranked information from DB: %v\n", err)
		http.Error(w, "No data found", http.StatusNotFound)
		return
	}
	defer rows.Close()

	// Places the cursor on the first row
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			log.Printf("Failed to read summoner ranked information from DB: %v\n", err)
			http.Error(w, "Database query failed", http.StatusInternalServerError)
			return
		}
		log.Printf("No summmoner data for PUUID: %s\n", unpackedRequest.Puuid)
		// Return a valid (empty) protobuf message. Returning http.Error here
		// makes the test client fail to unmarshal the response.
		response := &platformProto.UserProfile{Puuid: unpackedRequest.Puuid}
		data, marshalErr := proto.Marshal(response)
		if marshalErr != nil {
			log.Printf("Failed to marshal response: %v\n", marshalErr)
			http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	// Assigns the values of the row to mutable variables we can work with in GO
	var puuid, riotName, riotTag string
	var rank, wins, losses int
	if err := rows.Scan(&puuid, &riotName, &riotTag, &rank, &wins, &losses); err != nil {
		log.Printf("Failed to scan row: %v\n", err)
		http.Error(w, "Unable to scan row", http.StatusNotFound)
		return
	}

	response := &platformProto.UserProfile{
		Puuid:    puuid,
		RiotName: riotName,
		RiotTag:  riotTag,
		Rank:     int32(rank),
		Wins:     int32(wins),
		Losses:   int32(losses),
	}

	data, err := proto.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal response: %v\n", err)
		http.Error(w, "Failed to marshal response", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

func GetMatchHistory(w http.ResponseWriter, r *http.Request) {
	var unpackedRequest platformProto.MatchHistory
	if err := UnpackRequest(w, r, &unpackedRequest); err != nil {
		log.Printf("Could not unpack the payload: %s\n", err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbOpTimeout)
	defer cancel()

	// Actual query sent to postgres. The ORDER BY + LIMIT keep the cost
	// bounded no matter how long the player has been in the load test; the
	// composite (puuid, gameEndTimestamp DESC, matchID DESC) index in
	// dbScripts/init.sql lets Postgres stop after matchHistoryLimit rows
	// instead of reading the player's whole history.
	rows, err := dbPool.Query(ctx,
		`SELECT "matchID", "gameVer", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants" FROM "matchHistory" WHERE "puuid" = $1 ORDER BY "gameEndTimestamp" DESC, "matchID" DESC LIMIT $2`,
		unpackedRequest.PlayerPUUID, matchHistoryLimit)
	if err != nil {
		log.Printf("Failed to fetch match history from DB: %v\n", err)
		http.Error(w, "Failed to fetch match history", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// Actual struct we are populating and gonna return
	var matchHistories []*platformProto.MatchResult

	// Loop through all of the rows we found from the postgres query
	for rows.Next() {
		var matchID, gameVer string
		var gameDuration, gameCreationTimestamp, gameEndTimestamp int
		var teamOnePUUID, teamTwoPUUID []string
		var participantsJSON []byte

		// Scan the fields we searched for into mutable variables with specified types
		if err := rows.Scan(&matchID, &gameVer, &gameDuration, &gameCreationTimestamp, &gameEndTimestamp, &teamOnePUUID, &teamTwoPUUID, &participantsJSON); err != nil {
			log.Printf("Failed to scan row: %v\n", err)
			continue
		}

		gameStartTime := fmt.Sprintf("%d", gameCreationTimestamp)
		gameEndTime := fmt.Sprintf("%d", gameEndTimestamp)

		// This part kicked my ass a bit
		// Due to the very specific structure of the participants field in the PostgreSQL database
		// the structure of the datastructure and how it is unmarshaled is very specific.
		// List of JSON objects caused a few problems, this works though
		var participantList platformProto.ParticipantList
		if err := json.Unmarshal(participantsJSON, &participantList.Participants); err != nil {
			log.Printf("Failed to unmarshal participants into protobuf struct: %v\n", err)
			continue
		}

		var winners string

		// Load up MatchResult object with all of the entries we found and its relevant info
		matchHistories = append(matchHistories, &platformProto.MatchResult{
			MatchID:       matchID,
			GameVersion:   gameVer,
			GameDuration:  fmt.Sprintf("%d", gameDuration),
			GameStartTime: gameStartTime,
			GameEndTime:   gameEndTime,
			TeamOnePUUID:  teamOnePUUID,
			TeamTwoPUUID:  teamTwoPUUID,
			Participants:  participantList.Participants,
			Winners:       winners,
		})
	}

	if err := rows.Err(); err != nil {
		// A mid-stream error (most commonly the query deadline) means the
		// result set is incomplete. Returning a partial 200 makes the client
		// think the read succeeded; surface it as a real failure instead.
		log.Printf("Error iterating through rows: %v\n", err)
		http.Error(w, "Failed to fetch match history", http.StatusInternalServerError)
		return
	}

	// Prepare the response
	response := &platformProto.MatchHistory{
		PlayerPUUID: unpackedRequest.PlayerPUUID,
		Matches:     matchHistories,
	}

	// Marshal the response to protobuf format
	data, err := proto.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal response: %v\n", err)
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	// Send the response back to the client
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
