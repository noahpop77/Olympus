package gameServer

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/noahpop77/Olympus/game_server_service/game_server/gameServerProto"
	"google.golang.org/protobuf/proto"
)

func UnpackRequest(w http.ResponseWriter, r *http.Request, protoMessage proto.Message) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("invalid method")
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("error reading body: %v", err)
	}

	// Unmarshal into the provided proto message type
	err = proto.Unmarshal(data, protoMessage)
	if err != nil {
		return fmt.Errorf("error unmarshalling data: %v", err)
	}

	return nil
}

/*
- Unpacks connection requests - Difference from UnpackCreationRequest is
that Creation request contains a single matchID and all user PUUIDs.

- Connection comes in from the server
*/
func UnpackCreationRequest(w http.ResponseWriter, r *http.Request) (*gameServerProto.MatchCreation, error) {
	var unpackedRequest gameServerProto.MatchCreation
	err := UnpackRequest(w, r, &unpackedRequest)
	if err != nil {
		return nil, err
	}
	return &unpackedRequest, nil
}

/*
- Unpacks connection requests - Difference from UnpackCreationRequest is
that connection request contains a single matchID and the users PUUID

- Connection comes in from the server
*/
func UnpackConnectionRequest(w http.ResponseWriter, r *http.Request) (*gameServerProto.MatchConnection, error) {
	var unpackedRequest gameServerProto.MatchConnection
	err := UnpackRequest(w, r, &unpackedRequest)
	if err != nil {
		return nil, err
	}
	return &unpackedRequest, nil
}

func ConnectPlayerToMatch(activeMatches *sync.Map, match *gameServerProto.MatchCreation) (*gameServerProto.MatchResult, error) {
	
	// Main loop tracking if player has connected or not
	for {
		if len(match.ParticipantsPUUID) == 10 {
			// Used in calculating the creation time and duration inside of the generated rnadom match
			gameCreationUnixTime := time.Now().Unix()

			source := rand.NewSource(time.Now().UnixNano())
			r := rand.New(source)
			min := 100
			max := 500
			randomInt := r.Intn(max-min+1) + min

			// Simulated game timer
			time.Sleep(time.Duration(randomInt) * time.Second)

			// Generated randomized match data
			completedMatch := generateRandomMatchData(gameCreationUnixTime, match)
			return completedMatch, nil
		}

		// Arbitrary sleep to not eat up too much cpu resource
		time.Sleep(250 * time.Millisecond)
	}
}