package matchmaking

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/noahpop77/Olympus/matchmaking/party"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

func WithinRankRange (myRank, teamRank int) bool {
	diff := myRank - teamRank
	if diff < 4 && diff > -4 {
		return true
	} else {
		return false
	}
}


func MatchmakingSelection(w http.ResponseWriter, unpackedRequest *party.PartyRequest, rdb *redis.Client, ctx context.Context) {

	var matchedPlayers []string
	var matchedPlayerRanks []string

	keys, err := rdb.Keys(ctx,"*").Result()
	if err != nil {
		log.Fatalf("could not retrieve keys: %v", err)
	}	

	for _, key := range keys{

		hashData, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("failed to get hash data for key %s: %v", key, err)
			continue
		}
		if len(hashData) == 0 {
			// Skip if hash is empty.
			continue
		}

		// Essentially builds a map out of data we receive and set the values of playerInfo with it
		var playerInfo party.PartyRequest
		playerInfo.Participants = make([]*party.Participant, 1)
		playerInfo.Participants[0] = &party.Participant{
			RiotName: hashData["riotName"],
			RiotTagLine: hashData["riotTag"],
			Rank: hashData["rank"],
			Role: hashData["role"],
			Puuid: hashData["puuid"],
		}

		// These gotta be ints
		myRank, _ := strconv.Atoi(unpackedRequest.Participants[0].Rank)
		teammateRank, _ := strconv.Atoi(playerInfo.Participants[0].Rank)
		
		// If its a valid rank we use it
		if WithinRankRange(myRank, teammateRank) {
			matchedPlayerRanks = append(matchedPlayerRanks, playerInfo.Participants[0].Rank)
			matchedPlayers = append(matchedPlayers, playerInfo.Participants[0].Puuid)
		}
		
		if len(matchedPlayers) == 9 {
			fmt.Println("I AM RANK " + strconv.Itoa(myRank))
			for i := 0; i < len(matchedPlayers); i++ {
				fmt.Printf("Team Member %s:\t%d\t%s\t%s\n", strconv.Itoa(i), myRank, matchedPlayerRanks[i], matchedPlayers[i])
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Ranked team found!\n"))
			return

		} else {
			continue
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("No possible ranked team to be found...\n"))
}

// Unpacks the sent in party datastructure which is a protobuff. Careful with the .proto file
func UnpackRequest(w http.ResponseWriter, r *http.Request, unpackedRequest *party.PartyRequest) {
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
	err = proto.Unmarshal(data, unpackedRequest)
	if err != nil {
		http.Error(w, "Failed to unmarshal Protobuf data", http.StatusBadRequest)
		return
	}
}

func PartyHandler(w http.ResponseWriter, unpackedRequest *party.PartyRequest, rdb *redis.Client, ctx context.Context) {

	// Participants hash key & party information hash key
	participantKey := unpackedRequest.PartyId + ":participants:1"
	partyKey := unpackedRequest.PartyId + ":1"

	// Searches for the riotName of the user sending the matchmaking request in that party code
	// Does it exist?
	err := rdb.HGet(ctx, participantKey, "riotName").Err()
	if err != nil && err != redis.Nil {
		log.Fatalf("could not set participant info: %v", err)
	}

	// If it doesnt exist, set the party information and the participant information within that party else we hit the cache so ezpz.
	if err == redis.Nil {

		// Outer party info
		err = rdb.HSet(ctx, partyKey, "partyId", unpackedRequest.PartyId, "teamCount", unpackedRequest.TeamCount, "queueType", unpackedRequest.QueueType).Err()
		if err != nil {
			log.Fatalf("could not set participant info: %v", err)
		}
		
		// Inner participant info
		err = rdb.HSet(ctx, participantKey, "riotName", unpackedRequest.Participants[0].RiotName, "riotTag", unpackedRequest.Participants[0].RiotTagLine, "rank", unpackedRequest.Participants[0].Rank, "role", unpackedRequest.Participants[0].Role, "puuid", unpackedRequest.Participants[0].Puuid).Err()
		if err != nil {
			log.Fatalf("could not set participant info: %v", err)
		}

		fmt.Println("Redis Cache Miss")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Party request received successfully - Redis Cache Miss\n"))
	} else {
		fmt.Println("Redis Cache Hit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Party request received successfully - Redis Cache Hit\n"))
	}
	
}
