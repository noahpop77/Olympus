package matchmaking

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/noahpop77/Olympus/matchmaking/party"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

// WithinRankRange checks if the rank difference is within ±3.
func WithinRankRange(myRank, teamRank int) bool {
	diff := myRank - teamRank
	return diff < 4 && diff > -4
}

// SimulateQueueTimer simulates a timer while the player is in queue.
func SimulateQueueTimer(w http.ResponseWriter, r *http.Request, unpackedRequest *party.Players) {
	for i := 1; i <= 10; i++ {
		time.Sleep(1 * time.Second)
		if i%5 == 0 {
			fmt.Printf("%s Queue Timer: %d\n", unpackedRequest.Player1RiotName, i)
		}
	}
}

// matchedParty holds information about a party match.
type matchedParty struct {
	Key        string
	Puuid      string
	PlayerRank string
}

// processParty processes a single party by verifying rank constraints and performing an atomic update.
// If the party is a valid match, it adds its information (including its key) to the matched slice.
func processParty(ctx context.Context, rdb *redis.Client, myRank int, key string, matches *[]matchedParty) error {
	// Get the party hash data.
	hashData, err := rdb.HGetAll(ctx, key).Result()
	if err != nil {
		log.Printf("failed to get hash data for key %s: %v", key, err)
		return err
	}
	if len(hashData) == 0 {
		return nil
	}

	// Build the player info.
	playerRankStr := hashData["Player1Rank"]

	if playerRankStr == "" {
		// Skip processing if no valid rank is present
		return nil
	}
	teammateRank, err := strconv.Atoi(playerRankStr)
	if err != nil {
		return err
	}

	// Check rank constraints.
	if WithinRankRange(myRank, teammateRank) {
		*matches = append(*matches, matchedParty{
			Key:        key,
			Puuid:      hashData["Player1Puuid"],
			PlayerRank: playerRankStr,
		})
	}

	// // Demonstration: update a field atomically using WATCH.
	// txnFunc := func(tx *redis.Tx) error {
	// 	pipe := tx.Pipeline()
	// 	pipe.HSet(ctx, key, "Player2Puuid", "new_player_puuid")
	// 	_, err := pipe.Exec(ctx)
	// 	return err
	// }

	// // Retry mechanism to handle potential race conditions.
	// for i := 0; i < 5; i++ {
	// 	err = rdb.Watch(ctx, txnFunc, key)
	// 	if err == nil {
	// 		break
	// 	}
	// 	time.Sleep(100 * time.Millisecond)
	// }
	return err
}

// MatchmakingSelection concurrently processes all parties to build a matched team.
// When a team is found (9 matches in addition to the initiating party), the corresponding Redis keys are deleted.
func MatchmakingSelection(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context) {
	myRank, err := strconv.Atoi(unpackedRequest.Player1Rank)
	if err != nil {
		http.Error(w, "Invalid player rank", http.StatusBadRequest)
		return
	}

	var matches []matchedParty
	var mu sync.Mutex

	mu.Lock()

	keys, err := rdb.Keys(ctx, "*").Result()
	if err != nil {
		log.Fatalf("could not retrieve keys: %v", err)
	}

	// Process keys concurrently.
	for _, key := range keys {
		err := processParty(ctx, rdb, myRank, key, &matches)
		if err != nil {
			log.Printf("Error processing key %s: %v", key, err)
		}
	}

	// Remove matched parties from Redis so they cannot be reused.
	var delKeys []string
	for i := 0; i < len(matches); i++ {
		delKeys = append(delKeys, matches[i].Key)
		if len(delKeys) == 9 {
			break
		}
	}

	if len(delKeys) != 9 {
		return
	}

	// Using DEL command to remove all selected parties.
	if err := rdb.Del(ctx, delKeys...).Err(); err != nil {
		log.Printf("Error deleting matched keys: %v", err)
	}

	mu.Unlock()

	// Check if we have reached the desired team size.
	// Note: This example assumes that the initiating party is not part of the candidate list,
	// so we need 9 additional players.
	var printMutex sync.Mutex
	printMutex.Lock()
	if len(matches) >= 9 {
		// fmt.Println("\n-----------------------------------------------------")
		// fmt.Printf("Querying User PUUID: %s\n", unpackedRequest.Player1Puuid)
		// for i := 0; i < 9; i++ {
		// 	fmt.Printf("Team Member %d:\tMy Rank: %d - %s : %s\n",
		// 		i, myRank, matches[i].PlayerRank, matches[i].Puuid)
		// }

		var matchmadeTeam string
		for i := 0; i < 9; i++ {
			matchmadeTeam += fmt.Sprintf("Team Member %d:\tMy Rank: %d - %s : %s\n",
				i, myRank, matches[i].PlayerRank, matches[i].Puuid)
		}
		matchmadeTeam += fmt.Sprintf("ME           :  My Rank       %d : %s\n", myRank, unpackedRequest.Player1Puuid)

		w.Write([]byte("Ranked team found!\n"))
		w.Write([]byte(matchmadeTeam))
	} else {
		w.Write([]byte("No ranked team found...\n"))
	}
	printMutex.Unlock()
}

// UnpackRequest unpacks the Protobuf data into a party.Players structure.
func UnpackRequest(w http.ResponseWriter, r *http.Request, unpackedRequest *party.Players) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	err = proto.Unmarshal(data, unpackedRequest)
	if err != nil {
		http.Error(w, "Failed to unmarshal Protobuf data", http.StatusBadRequest)
		return
	}
}

// PartyHandler handles party creation and Redis caching for the matchmaking request.
func PartyHandler(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context) {
	// Check if the party already exists.
	err := rdb.HGet(ctx, unpackedRequest.PartyId, "PartyId").Err()
	if err != nil && err != redis.Nil {
		log.Fatalf("could not set participant info: %v", err)
	}

	// If the party doesn't exist, create it and add to the matchmaking set.
	if err == redis.Nil {
		err = rdb.HSet(ctx, unpackedRequest.PartyId,
			"PartyId", unpackedRequest.PartyId,
			"TeamCount", unpackedRequest.TeamCount,
			"QueueType", unpackedRequest.QueueType,

			"Player1Puuid", unpackedRequest.Player1Puuid,
			"Player1RiotName", unpackedRequest.Player1RiotName,
			"Player1RiotTagLine", unpackedRequest.Player1RiotTagLine,
			"Player1Rank", unpackedRequest.Player1Rank,
			"Player1Role", unpackedRequest.Player1Role,

			"Player2Puuid", unpackedRequest.Player2Puuid,
			"Player2RiotName", unpackedRequest.Player2RiotName,
			"Player2RiotTagLine", unpackedRequest.Player2RiotTagLine,
			"Player2Rank", unpackedRequest.Player2Rank,
			"Player2Role", unpackedRequest.Player2Role).Err()

		if err != nil {
			log.Fatalf("could not set participant info: %v", err)
		}

		// err = rdb.SAdd(ctx, "MatchmakingSet", unpackedRequest.PartyId).Err()
		// if err != nil {
		// 	log.Fatalf("Could not properly configure the matchmaking set: %v", err)
		// }

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Player added to queue...\n"))
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Player added to queue...\n"))
	}
}
