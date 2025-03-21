package gameServer

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/noahpop77/Olympus/game_server_service/game_server/gameServerProto"
	"google.golang.org/protobuf/proto"
)

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

func UpdateProfile(conn *pgx.Conn, puuid string, riotName string, riotTag string, randomMatch *gameServerProto.MatchResult) {

	// TODO: VALIDATE THAT THE RANK, WIN, AND LOSS INCREMENT/DECREMENT IS
	// WORKING PROPERLY IN THE summonerRankedInfo TABLE

	// TODO: USE PASSED IN OR DATABASE VALIDATED RANK RATHER THAN THE
	// DEFAULT ONE

	var myTeam string
	for _, value := range randomMatch.TeamOnePUUID{
		if value == puuid {
			myTeam = "one"
		}
	}
	if myTeam != "one" {
		for _, value := range randomMatch.TeamTwoPUUID{
			if value == puuid {
				myTeam = "two"
			}
		}
	}	

	var rank, wins, losses int
	err := conn.QueryRow(context.Background(),
		`SELECT rank, wins, losses FROM "summonerRankedInfo" WHERE puuid = $1`, puuid).
		Scan(&rank, &wins, &losses)
	log.Printf("err: %v: %d, %d, %d, %s",err, rank, wins, losses, puuid)
	if err == pgx.ErrNoRows{
		// defaults: rank=22 wins=0, losses=0
		if myTeam == randomMatch.Winners {
			rank = 23
			wins = 1
			losses = 0
		} else {
			rank = 21
			wins = 0
			losses = 1
		}

	} else if err != nil && err != pgx.ErrNoRows {
		log.Fatal("Failed to fetch summoner rank info:", err)
	}

	if myTeam == randomMatch.Winners {
		rank++
		wins++
	} else {
		if rank > 0 {
			rank--
		}
		losses++
	}


	_, err = conn.Exec(context.Background(),
		`INSERT INTO "summonerRankedInfo" 
		("puuid", "riotName", "riotTag", "rank", "wins", "losses") 
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (puuid) 
		DO UPDATE
		SET "riotName" = EXCLUDED."riotName", 
        "riotTag" = EXCLUDED."riotTag", 
        "rank" = EXCLUDED."rank", 
        "wins" = EXCLUDED."wins", 
        "losses" = EXCLUDED."losses";`,
		puuid, riotName, riotTag, rank, wins, losses)
	
		if err != nil {
			log.Fatalf("Insert failed: %v\n", err)
		}
}


// Main functional loop handling connection function
func ConnectPlayerToMatch(activeMatches *sync.Map, matchDataMap *sync.Map, match *gameServerProto.MatchCreation, matchParticipantsMap *sync.Map, unpackedRequest *gameServerProto.MatchConnection) error {
	
	// Main loop tracking if player has connected or not
	for {
		if len(match.ParticipantsPUUID) != 10 {
			// Arbitrary sleep to not eat up too much cpu resource
			time.Sleep(250 * time.Millisecond)
			continue
		} else {
			
			generateGameData(match, matchParticipantsMap, matchDataMap, unpackedRequest)

			for {
				time.Sleep(250 * time.Millisecond)

				participantValue, ok := matchParticipantsMap.Load(match.MatchID)
				if !ok {
					log.Printf("Failed to load participants for match ID %s\n", match.MatchID)
					continue
				}

				var randomParticipants []*gameServerProto.Participant
				if participantValue != nil {
					randomParticipants, ok = participantValue.([]*gameServerProto.Participant)
					if !ok {
						// Handle type assertion failure
						log.Printf("Invalid participant data for match ID %s\n", match.MatchID)
						continue
					}
				}

				if len(randomParticipants) != 10 {
					continue
				} else {
					value, _ := matchDataMap.Load(match.MatchID)
					var matchData *gameServerProto.MatchResult
					if value != nil {
						matchData = value.(*gameServerProto.MatchResult)
					}
					
					gameDuration, _ := strconv.Atoi(matchData.GameDuration)
					// gameDuration := 1
					time.Sleep(time.Duration(gameDuration) * time.Second)
					break
				}
			}

			return nil
		
		}
	}
}