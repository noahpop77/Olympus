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
	
	// fmt.Println(&request)

	val, err := rdb.Get(ctx, request.PartyId).Result()
	if err != nil && err != redis.Nil {
		panic(err)
	}

	// Set the HSET commands
	// Party Info
	soloID := request.PartyId + ":1"
	err = rdb.HSet(ctx, soloID, "partyId", request.PartyId, "teamCount", request.TeamCount, "queueType", request.QueueType).Err()
	if err != nil {
		log.Fatalf("could not set party info: %v", err)
	}

	// Participant Info
	err = rdb.HSet(ctx, request.PartyId + ":participants:1", 
		"riotName", request.Participants[0].RiotName, 
		"riotTag", request.Participants[0].RiotTagLine, 
		"rank", request.Participants[0].Rank, 
		"puuid", request.Participants[0].Puuid).Err()
	if err != nil {
		log.Fatalf("could not set participant info: %v", err)
	}
	// fmt.Println("Participant info set successfully")

	if err == redis.Nil {
		//string(body) is the stringified JSON payload sent to the endpoint
		err = rdb.Set(ctx, request.PartyId, string(data), 0).Err()
		if err != nil {
			panic(err)
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
//curl -X POST -H "Content-Type: application/x-protobuf" --data-binary @partyRequest.bin http://localhost:8080/party
