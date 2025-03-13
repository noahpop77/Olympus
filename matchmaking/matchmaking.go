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

type PartyResources struct {
	CancelFunc context.CancelFunc
	Writer     http.ResponseWriter
}

// matchedParty holds information about a party match.
type matchedParty struct {
	Player1RiotName string
	Key             string
	Puuid           string
	PlayerRank      string
}

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

// UnpackRequest unpacks the Protobuf data into a party.Players structure.
func UnpackRequest(w http.ResponseWriter, r *http.Request) *party.Players {
	if r.Method != http.MethodPost {
		return nil
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}

	var unpackedRequest party.Players
	err = proto.Unmarshal(data, &unpackedRequest)
	if err != nil {
		return nil
	}

	return &unpackedRequest
}

func UnpackedRequestValidation(unpackedRequest *party.Players) bool {

	switch {
	// This case must happen before unpackedRequest.PartyId[:6] != "PARTY_" otherwise you will get the error:
	// slice bounds out of range [:6] with length 0
	case len(unpackedRequest.PartyId) < 7:
		return false
	case unpackedRequest.PartyId[:6] != "PARTY_":
		return false
	case unpackedRequest.QueueType != 420:
		return false
	
	case len(unpackedRequest.Player1Puuid) < 20:
		return false
	case len(unpackedRequest.Player1RiotName) < 4:
		return false
	case len(unpackedRequest.Player1RiotTagLine) < 2:
		return false
	case unpackedRequest.Player1Rank > 44 && unpackedRequest.Player1Rank < 0:
		return false
	case unpackedRequest.Player1Role != "Middle" && unpackedRequest.Player1Role != "Top" && unpackedRequest.Player1Role != "Jungle" && unpackedRequest.Player1Role != "Bottom" && unpackedRequest.Player1Role != "Support":
		return false
	
	case len(unpackedRequest.Player2Puuid) < 20 && unpackedRequest.TeamCount != 1:
		return false
	case len(unpackedRequest.Player2RiotName) < 4 && unpackedRequest.TeamCount != 1:
		return false
	case len(unpackedRequest.Player2RiotTagLine) < 2 && unpackedRequest.TeamCount != 1:
		return false
	case unpackedRequest.Player2Rank > 44 && unpackedRequest.Player2Rank < 0 && unpackedRequest.TeamCount != 1:
		return false
	case unpackedRequest.Player2Role != "Middle" && unpackedRequest.Player2Role != "Top" && unpackedRequest.Player2Role != "Jungle" && unpackedRequest.Player2Role != "Bottom" && unpackedRequest.Player2Role != "Support" && unpackedRequest.TeamCount != 1:
		return false
	
	default:
		return true
	}
}

// AddPartyToRedis handles party creation and Redis caching for the matchmaking request.
func AddPartyToRedis(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context) {
	
	err := rdb.HSet(ctx, unpackedRequest.PartyId,
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
		log.Printf("could not set participant info: %v", err)
	}
}

// Deletes ParyIDs from Redis for players who cancel queue
func RemovePartyFromRedis(partyId string, rdb *redis.Client, ctx context.Context) {
	err := rdb.Del(ctx, partyId).Err()
	if err != nil {
		log.Printf("could not delete participant info: %v", err)
	}
}

// processParty processes a single party by verifying rank constraints and performing an atomic update.
// If the party is a valid match, it adds its information (including its key) to the matched slice.
func processParty(ctx context.Context, rdb *redis.Client, unpackedRequest *party.Players, key string, matches *[]matchedParty) error {

	tempRank, err := rdb.HGet(ctx, key, "Player1Rank").Result()
	if err != nil {
		log.Printf("failed to get hash data for key %s: %v", key, err)
		return err
	}
	if len(tempRank) == 0 {
		return nil
	}

	teammateRank, err := strconv.Atoi(tempRank)

	// Check rank constraints.
	if WithinRankRange(int(unpackedRequest.Player1Rank), teammateRank) {
		hashData, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("failed to get hash data for key %s: %v", key, err)
			return err
		}
		if hashData["Player1RiotName"] == unpackedRequest.Player1RiotName {
			return nil
		}

		*matches = append(*matches, matchedParty{
			Player1RiotName: hashData["Player1RiotName"],
			Key:             key,
			Puuid:           hashData["Player1Puuid"],
			PlayerRank:      hashData["Player1Rank"],
		})
		return nil
	}

	return err
}

// MatchmakingSelection concurrently processes all parties to build a matched team.
// When a team is found (9 matches in addition to the initiating party), the corresponding Redis keys are deleted.
func MatchmakingSelection(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context, partyResourcesMap *sync.Map, mu *sync.Mutex) bool {
	var matchedParties []matchedParty

	// Main mutex
	mu.Lock()
	defer mu.Unlock()

	// Takes steps as its scanning through the database of 100 keys at a time to not
	// lock up the Redis database if we were to scan the whole thing in 1 go for no
	// reason. 100 keys at a time steps.
	var newCursor uint64
	for len(matchedParties) < 9 {
		keys, nextCursor, err := rdb.Scan(ctx, newCursor, "*", 100).Result()
		if err != nil {
			log.Printf("Error scanning keys: %v", err)
			return false
		}

		for _, key := range keys {
			if len(matchedParties) == 9 {
				break
			}
			err := processParty(ctx, rdb, unpackedRequest, key, &matchedParties)
			if err != nil {
				log.Printf("Error processing key %s: %v", key, err)
				return false
			}
		}

		newCursor = nextCursor
		if newCursor == 0 || len(matchedParties) == 9 {
			break
		}
	}

	// Remove matched parties from Redis so they cannot be reused.
	var delKeys []string
	for i := 0; i < len(matchedParties); i++ {
		delKeys = append(delKeys, matchedParties[i].Key)
		if len(delKeys) == 9 {
			break
		}
	}

	if len(delKeys) != 9 {
		return false
	}

	// Deletes keys for teammates
	if err := rdb.Del(ctx, delKeys...).Err(); err != nil {
		log.Printf("Error deleting matched keys: %v", err)
		return false
	}
	// Deletes keys for Anchor being
	if err := rdb.Del(ctx, unpackedRequest.PartyId).Err(); err != nil {
		log.Printf("Error anchor being: %v", err)
		return false
	}

	responseText := fmt.Sprintf("Match found for %s! - ", unpackedRequest.Player1RiotName)
	for i := 0; i < 9; i++ {
		responseText += fmt.Sprintf("%s, ", matchedParties[i].Player1RiotName)
	}
	responseText += "\n"

	w.Write([]byte(responseText))

	// Finishes off and cleans up the connections for teammates
	for _, partyKey := range delKeys {
		if partyCtx, ok := partyResourcesMap.Load(partyKey); ok {
			if ctx, ok := partyCtx.(PartyResources); ok {
				ctx.Writer.Write([]byte(responseText))
				ctx.CancelFunc()
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	return true

}

func MatchFinder(w http.ResponseWriter, unpackedRequest *party.Players, rdb *redis.Client, ctx context.Context, partyResourcesMap *sync.Map, matchmakingContext context.Context, requester *http.Request, mu *sync.Mutex) {

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Main event loop for matchmaking
	for {
		select {
		// Other player uses you as a team mate
		case <-matchmakingContext.Done():
			return

			// Queue is canceled by player
		case <-requester.Context().Done():
			RemovePartyFromRedis(unpackedRequest.PartyId, rdb, ctx)
			return

		// Notifies client on predefined timer to not eat all compute resources
		case <-ticker.C:

			lfgResponse := fmt.Sprintf("Looking for match for %s...\n", unpackedRequest.Player1RiotName)
			_, err := w.Write([]byte(lfgResponse))
			if err != nil {
				return
			}
			flusher.Flush()

			if MatchmakingSelection(w, unpackedRequest, rdb, ctx, partyResourcesMap, mu) {
				flusher.Flush()
				return
			}
		}

	}

}
