package matchmaking

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/noahpop77/Olympus/matchmaking_service/matchmaking/matchmakingProto"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type PartyResources struct {
	CancelFunc context.CancelFunc
	Writer     http.ResponseWriter
}

// matchedParty holds information about a party match.
type matchedParty struct {
	PlayerRiotName 	string
	Key             string
	Puuid           string
	PlayerRank      string
}

// WithinRankRange checks if the rank difference is within ±3.
func WithinRankRange(myRank, teamRank int) bool {
	diff := myRank - teamRank
	return diff < 4 && diff > -4
}

// UnpackRequest unpacks the Protobuf data into a party.Players structure.
func UnpackRequest(w http.ResponseWriter, r *http.Request) *matchmakingProto.Players {
	if r.Method != http.MethodPost {
		return nil
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}

	var unpackedRequest matchmakingProto.Players
	err = proto.Unmarshal(data, &unpackedRequest)
	if err != nil {
		return nil
	}

	return &unpackedRequest
}

func UnpackedRequestValidation(unpackedRequest *matchmakingProto.Players) bool {

	switch {
	// This case must happen before unpackedRequest.PartyId[:6] != "PARTY_" otherwise you will get the error:
	// slice bounds out of range [:6] with length 0
	case len(unpackedRequest.PartyId) < 7:
		return false
	case unpackedRequest.PartyId[:6] != "PARTY_":
		return false
	case unpackedRequest.QueueType != 420:
		return false
	case len(unpackedRequest.PlayerPuuid) < 20:
		return false
	case len(unpackedRequest.PlayerRiotName) < 4:
		return false
	case len(unpackedRequest.PlayerRiotTagLine) < 2:
		return false
	case unpackedRequest.PlayerRank > 44 && unpackedRequest.PlayerRank < 0:
		return false
	case unpackedRequest.PlayerRole != "Middle" && unpackedRequest.PlayerRole != "Top" && unpackedRequest.PlayerRole != "Jungle" && unpackedRequest.PlayerRole != "Bottom" && unpackedRequest.PlayerRole != "Support":
		return false
	
	default:
		return true
	}
}

// AddPartyToRedis handles party creation and Redis caching for the matchmaking request.
func AddPartyToRedis(w http.ResponseWriter, unpackedRequest *matchmakingProto.Players, rdb *redis.Client, ctx context.Context) {
	
	err := rdb.HSet(ctx, unpackedRequest.PartyId,
		"PartyId", unpackedRequest.PartyId,
		"QueueType", unpackedRequest.QueueType,
		"PlayerPuuid", unpackedRequest.PlayerPuuid,
		"PlayerRiotName", unpackedRequest.PlayerRiotName,
		"PlayerRiotTagLine", unpackedRequest.PlayerRiotTagLine,
		"PlayerRank", unpackedRequest.PlayerRank,
		"PlayerRole", unpackedRequest.PlayerRole).Err()

	if err != nil {
		log.Printf("could not set participant info: %v", err)
	}
}

// Deletes ParyIDs from Redis for players who cancel queue
func RemovePartyFromRedis(partyId string, rdb *redis.Client, ctx context.Context) {
	err := rdb.Del(ctx, partyId).Err()
	if err != nil {
		log.Printf("could not delete: %v", err)
	}
}

func ProvisionGameServer(matchData []byte) bool {
	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest("POST", "http://game_server:8081/spawnMatch", bytes.NewBuffer(matchData))
	if err != nil {
		fmt.Println("Failed to create request:", err)
		return false
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Request failed:", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200{
		return true
	} else {
		return false
	}
}

// ProcessParty processes a single party by verifying rank constraints and performing an atomic update.
// If the party is a valid match, it adds its information (including its key) to the matched slice.
func ProcessParty(ctx context.Context, rdb *redis.Client, unpackedRequest *matchmakingProto.Players, key string, matches *[]matchedParty) error {

	tempRank, err := rdb.HGet(ctx, key, "PlayerRank").Result()
	if err != nil {
		// log.Printf("failed to hget data for key %s: %v", key, err)
		return err
	}

	if len(tempRank) == 0 {
		return nil
	}

	teammateRank, err := strconv.Atoi(tempRank)

	// Check rank constraints.
	if WithinRankRange(int(unpackedRequest.PlayerRank), teammateRank) {
		hashData, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("failed to get hash data for key %s: %v", key, err)
			return err
		}
		if hashData["PlayerRiotName"] == unpackedRequest.PlayerRiotName {
			return nil
		}

		*matches = append(*matches, matchedParty{
			PlayerRiotName: hashData["PlayerRiotName"],
			Key:             key,
			Puuid:           hashData["PlayerPuuid"],
			PlayerRank:      hashData["PlayerRank"],
		})
		return nil
	}

	return err
}

// MatchmakingSelection concurrently processes all parties to build a matched team.
// When a team is found (9 matches in addition to the initiating party), the corresponding Redis keys are deleted.
func MatchmakingSelection(w http.ResponseWriter, unpackedRequest *matchmakingProto.Players, rdb *redis.Client, ctx context.Context, partyResourcesMap *sync.Map, mu *sync.Mutex) bool {

	var matchedParties []matchedParty
	mu.Lock()
	defer mu.Unlock()

	// Steps through the db 100 keys at a time. Full Scans locks DB.
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
			err := ProcessParty(ctx, rdb, unpackedRequest, key, &matchedParties)
			if err == redis.Nil {
				continue
			} else if err != nil {
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


	for _, key := range delKeys {
		RemovePartyFromRedis(key, rdb, ctx)
	}
	RemovePartyFromRedis(unpackedRequest.PartyId, rdb, ctx)

	// Protobuf format for message 
	response := &matchmakingProto.MatchResponse{
		MatchID:      generateMatchID(),
		ParticipantsPUUID: []string{},
	}

	response.ParticipantsPUUID = append(response.ParticipantsPUUID, unpackedRequest.PlayerPuuid)
	for i := 0; i < 9; i++ {
		response.ParticipantsPUUID = append(response.ParticipantsPUUID, matchedParties[i].Puuid)
	}

	// data, err := json.Marshal(responseRequest)
	data, err := proto.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal responseRequest: %v", err)
		return false
	}

	if ProvisionGameServer(data) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write([]byte(data))

		// Finishes off and cleans up the connections for teammates
		for _, partyKey := range delKeys {
			if partyCtx, ok := partyResourcesMap.Load(partyKey); ok {
				if ctx, ok := partyCtx.(PartyResources); ok {
					ctx.Writer.Write([]byte(data))
					ctx.CancelFunc()
				}
			}
		}
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write([]byte("Failed to provision game server"))

		// Finishes off and cleans up the connections for teammates
		for _, partyKey := range delKeys {
			if partyCtx, ok := partyResourcesMap.Load(partyKey); ok {
				if ctx, ok := partyCtx.(PartyResources); ok {
					ctx.Writer.Write([]byte("Failed to provision game server"))
					ctx.CancelFunc()
				}
			}
		}
	}

	time.Sleep(100 * time.Millisecond)

	return true

}

func generateMatchID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	src := rand.NewSource(time.Now().UnixNano())
	r := rand.New(src)

	id := make([]byte, 10)
	for i := range id {
		id[i] = charset[r.Intn(len(charset))]
	}
	return "MATCH_" + string(id)
}

func MatchFinder(w http.ResponseWriter, unpackedRequest *matchmakingProto.Players, rdb *redis.Client, ctx context.Context, partyResourcesMap *sync.Map, matchmakingContext context.Context, requester *http.Request, mu *sync.Mutex) {

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(10 * time.Millisecond)
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

			// lfgResponse := fmt.Sprintf("Looking for match for %s...\n", unpackedRequest.PlayerRiotName)
			// _, err := w.Write([]byte(lfgResponse))
			// if err != nil {
			// 	return
			// }
			// flusher.Flush()

			if MatchmakingSelection(w, unpackedRequest, rdb, ctx, partyResourcesMap, mu) {
				flusher.Flush()
				return
			}
		}

	}

}
