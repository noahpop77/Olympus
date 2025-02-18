package matchmaking

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/noahpop77/Olympus/matchmaking/party"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)


func PartyHandler(w http.ResponseWriter, r *http.Request, rdb *redis.Client, ctx context.Context) {
	// Check if it's a POST request
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// Read the Protobuf data from the request body
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	// Unmarshal the Protobuf data into the PartyRequest struct
	var request party.PartyRequest
	err = proto.Unmarshal(data, &request)
	if err != nil {
		http.Error(w, "Failed to unmarshal Protobuf data", http.StatusBadRequest)
		return
	}

	val, err := rdb.Get(ctx, request.PartyId).Result()
	if err != nil && err != redis.Nil {
		panic(err)
	}


	// Participants hash key
	participantKey := request.PartyId + ":participants:1"
	
	// Party information hash key
	partyKey := request.PartyId + ":1"

	// Searches for the riotName of the user sending the matchmaking request in that party code
	// Does it exist?
	err = rdb.HGet(ctx, participantKey, "riotName").Err()
	if err != nil && err != redis.Nil {
		log.Fatalf("could not set participant info: %v", err)
	}

	// If it doesnt exist, set the party information and the participant information within that party else we hit the cache so ezpz.
	if err == redis.Nil {

		// Outer party info
		err = rdb.HSet(ctx, partyKey, "partyId", request.PartyId, "teamCount", request.TeamCount, "queueType", request.QueueType).Err()
		if err != nil {
			log.Fatalf("could not set participant info: %v", err)
		}
		
		// Inner participant info
		err = rdb.HSet(ctx, participantKey, "riotName", request.Participants[0].RiotName, "riotTag", request.Participants[0].RiotTagLine, "rank", request.Participants[0].Rank, "puuid", request.Participants[0].Puuid).Err()
		if err != nil {
			log.Fatalf("could not set participant info: %v", err)
		}

		fmt.Println("Redis Cache Miss")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Party request received successfully - Redis Cache Miss"))
		w.Write(data)
		return
	} else {
		fmt.Println("Redis Cache Hit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Party request received successfully - Redis Cache Hit"))
		w.Write([]byte(val))
		return
	}
	
}
