package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/noahpop77/Olympus/platform_service/platformProto"
	"google.golang.org/protobuf/proto"
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


func getMatchHistory(w http.ResponseWriter, r *http.Request) {
	var unpackedRequest platformProto.MatchHistory
	UnpackRequest(w, r, &unpackedRequest)

	// Connect to postgres database
	// TODO: Make it not hardcoded at some point
	dsn := "postgres://sawa:sawa@postgres:5432/olympus"
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer conn.Close(context.Background())

	// Actual query sent to postgres
	rows, err := conn.Query(context.Background(),
		`SELECT "matchID", "gameVer", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants" FROM "matchHistory" WHERE "puuid" = $1`, unpackedRequest.PlayerPUUID)
	if err != nil {
		log.Fatalf("Failed to fetch match history from DB: %v\n", err)
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

		gameStartTime := fmt.Sprintf("%d", gameCreationTimestamp)
		gameEndTime := fmt.Sprintf("%d", gameEndTimestamp)

		// Scan the fields we searched for into mutable variables with specified types
		if err := rows.Scan(&matchID, &gameVer, &gameDuration, &gameCreationTimestamp, &gameEndTimestamp, &teamOnePUUID, &teamTwoPUUID, &participantsJSON); err != nil {
			log.Printf("Failed to scan row: %v\n", err)
			continue
		}		

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
		log.Fatalf("Error iterating through rows: %v\n", err)
	}

	// Prepare the response
	response := &platformProto.MatchHistory{
		PlayerPUUID: unpackedRequest.PlayerPUUID,
		Matches:     matchHistories,
	}

	// Marshal the response to protobuf format
	data, err := proto.Marshal(response)
	if err != nil {
		log.Fatalf("Failed to marshal response: %v\n", err)
	}

	// Send the response back to the client
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
