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
	for i := 1; i <= 5; i++ {
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
	return err
}

// MatchmakingSelection concurrently processes all parties to build a matched team.
// When a team is found (9 matches in addition to the initiating party), the corresponding Redis keys are deleted.
func MatchmakingSelection(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context, partyCancels *sync.Map) bool {
	var matches []matchedParty
	var mu sync.Mutex

	mu.Lock()

	myRank, err := strconv.Atoi(unpackedRequest.Player1Rank)
	if err != nil {
		http.Error(w, "Invalid player rank", http.StatusBadRequest)
		return false
	}

	keys, err := rdb.Keys(ctx, "*").Result()
	if err != nil {
		log.Fatalf("could not retrieve keys: %v", err)
		return false
	}

	for _, key := range keys {
		err := processParty(ctx, rdb, myRank, key, &matches)
		if err != nil {
			log.Printf("Error processing key %s: %v", key, err)
			return false
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
		return false
	}

	if err := rdb.Del(ctx, delKeys...).Err(); err != nil {
		log.Printf("Error deleting matched keys: %v", err)
		return false
	}

	if len(matches) >= 9 {

		var matchmadeTeam string
		for i := 0; i < 9; i++ {
			matchmadeTeam += fmt.Sprintf("Team Member %d:\tMy Rank: %d - %s : %s\n", i, myRank, matches[i].PlayerRank, matches[i].Puuid)
		}

		matchmadeTeam += fmt.Sprintf("ME           :  My Rank       %d : %s\n", myRank, unpackedRequest.Player1Puuid)

		w.Write([]byte(matchmadeTeam))

		for _, partyKey := range delKeys {
			if cancel, ok := partyCancels.Load(partyKey); ok {
				cancel.(context.CancelFunc)()
				partyCancels.Delete(partyKey) 
				// fmt.Printf("Task %s canceled\n", partyKey)
			}
		}

		partyCancels.Range(func(key, _ interface{}) bool {
			fmt.Printf("%v, ", key)
			return true
		})
		fmt.Printf("\n\n")
		

		return true
	}
	mu.Unlock()

	return false
}

func MatchFinder(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context, partyCancels *sync.Map, matchmakingContext context.Context) {

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	for {
		select {
		case <- matchmakingContext.Done():
			fmt.Printf("Match found for %s\n", unpackedRequest.Player1RiotName)
			w.Write([]byte(fmt.Sprintf("Match found for %s\n", unpackedRequest.Player1RiotName)))
			return
		default:
			time.Sleep(1 * time.Second)
			lfgResponse := fmt.Sprintf("Looking for match for %s...\n", unpackedRequest.Player1RiotName)
			_, err := w.Write([]byte(lfgResponse))
			if err != nil {
				log.Printf("Client disconnected, stopping search for %s", unpackedRequest.Player1RiotName)
				return
			}
			flusher.Flush()

			if MatchmakingSelection(w, unpackedRequest, rdb, ctx, partyCancels) {
				flusher.Flush()
				return
			}
		}
	}
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
	}
}
