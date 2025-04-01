package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/noahpop77/Olympus/game_server_service/gameServerProto"
	"google.golang.org/protobuf/proto"
)

func PrintBanner(port string) {
	fmt.Println(`=================================================
██████╗  █████╗ ███╗   ███╗███████╗             
██╔════╝ ██╔══██╗████╗ ████║██╔════╝             
██║  ███╗███████║██╔████╔██║█████╗               
██║   ██║██╔══██║██║╚██╔╝██║██╔══╝               
╚██████╔╝██║  ██║██║ ╚═╝ ██║███████╗             
 ╚═════╝ ╚═╝  ╚═╝╚═╝     ╚═╝╚══════╝             				 
███████╗███████╗██████╗ ██╗   ██╗███████╗██████╗ 
██╔════╝██╔════╝██╔══██╗██║   ██║██╔════╝██╔══██╗
███████╗█████╗  ██████╔╝██║   ██║█████╗  ██████╔╝
╚════██║██╔══╝  ██╔══██╗╚██╗ ██╔╝██╔══╝  ██╔══██╗
███████║███████╗██║  ██║ ╚████╔╝ ███████╗██║  ██║
╚══════╝╚══════╝╚═╝  ╚═╝  ╚═══╝  ╚══════╝╚═╝  ╚═╝`)
	fmt.Println("=================================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("=================================================")
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

func UpdateProfile(conn *pgx.Conn, unpackedRequest *gameServerProto.MatchConnection, randomMatch *gameServerProto.MatchResult) {

	var myTeam string
	for _, value := range randomMatch.TeamOnePUUID {
		if value == unpackedRequest.ParticipantPUUID {
			myTeam = "one"
		}
	}
	if myTeam != "one" {
		for _, value := range randomMatch.TeamTwoPUUID {
			if value == unpackedRequest.ParticipantPUUID {
				myTeam = "two"
			}
		}
	}

	unpackedRank, _ := strconv.Atoi(unpackedRequest.Rank)

	var rank, wins, losses int
	err := conn.QueryRow(context.Background(),
		`SELECT rank, wins, losses FROM "summonerRankedInfo" WHERE puuid = $1`, unpackedRequest.ParticipantPUUID).
		Scan(&rank, &wins, &losses)
	if err == pgx.ErrNoRows {
		// defaults: rank=22 wins=0, losses=0
		rank = unpackedRank
		wins = 0
		losses = 0
	} else if err != nil && err != pgx.ErrNoRows {
		log.Fatal("Failed to fetch summoner rank info:", err)
	}

	if myTeam == randomMatch.Winners {
		if rank == 44 {
			wins++
		} else {
			rank++
			wins++
		}
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
		SET "rank" = EXCLUDED."rank", 
        "wins" = EXCLUDED."wins", 
        "losses" = EXCLUDED."losses";`,
		unpackedRequest.ParticipantPUUID, unpackedRequest.RiotName, unpackedRequest.RiotTag, rank, wins, losses)

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
					// value, _ := matchDataMap.Load(match.MatchID)
					// var matchData *gameServerProto.MatchResult
					// if value != nil {
					// 	matchData = value.(*gameServerProto.MatchResult)
					// }

					// gameDuration, _ := strconv.Atoi(matchData.GameDuration)
					gameDuration := 1
					time.Sleep(time.Duration(gameDuration) * time.Second)

					break
				}
			}

			return nil

		}
	}
}

// Endpoint that users will use to connect to the marked matches in the sync.Map
// Consumed by the /connectToMatch endpoint
func NewPlayerConnection(w http.ResponseWriter, r *http.Request, activeMatches *sync.Map, matchDataMap *sync.Map, matchParticipantsMap *sync.Map, databaseTransactionMutex *sync.Mutex) {
	unpackedRequest, err := UnpackConnectionRequest(w, r)
	if err != nil {
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	// Loads data for relevant match that is in the marked sync.Map
	validateSyncStore, ok := activeMatches.Load(unpackedRequest.MatchID)
	if ok {
		match, valid := validateSyncStore.(*gameServerProto.MatchCreation)
		if !valid {
			fmt.Println("Error: Type assertion failed")
			return
		}
		var wg sync.WaitGroup

		// Loops through match PUUIDs in requested match ID to find out if you are in it
		for _, value := range match.ParticipantsPUUID {
			if value == unpackedRequest.ParticipantPUUID {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := ConnectPlayerToMatch(activeMatches, matchDataMap, match, matchParticipantsMap, unpackedRequest)
					if err != nil {
						http.Error(w, "Failed to connect player to match", http.StatusInternalServerError)
						return
					}

					dsn := "postgres://sawa:sawa@postgres:5432/olympus"
					conn, err := pgx.Connect(context.Background(), dsn)
					if err != nil {
						log.Fatalf("Unable to connect to database: %v\n", err)
					}
					defer conn.Close(context.Background())

					value, _ := matchDataMap.Load(match.MatchID)
					// log.Printf("\n-----------------\n%s\n-----------------\n", value)
					var randomMatch *gameServerProto.MatchResult
					if value != nil {
						randomMatch = value.(*gameServerProto.MatchResult)
					}

					participantValue, _ := matchParticipantsMap.Load(match.MatchID)
					var randomParticipants []*gameServerProto.Participant
					if participantValue != nil {
						randomParticipants = participantValue.([]*gameServerProto.Participant)
					}

					participantJsonData, err := json.Marshal(randomParticipants)
					if err != nil {
						log.Fatalf("Failed to convert to JSON: %v", err)
					}

					// Define match data
					matchID := randomMatch.MatchID
					gameVer := randomMatch.GameVersion
					puuid := unpackedRequest.ParticipantPUUID
					gameDuration := randomMatch.GameDuration
					gameCreationTimestamp := randomMatch.GameStartTime
					gameEndTimestamp := randomMatch.GameEndTime
					teamOnePUUID := randomMatch.TeamOnePUUID
					teamTwoPUUID := randomMatch.TeamTwoPUUID
					participants := participantJsonData

					databaseTransactionMutex.Lock()
					UpdateProfile(conn, unpackedRequest, randomMatch)
					databaseTransactionMutex.Unlock()

					// Execute INSERT query
					_, err = conn.Exec(context.Background(),
						`INSERT INTO "matchHistory" 
						("matchID", "gameVer", "puuid", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants") 
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
						matchID, gameVer, puuid, gameDuration, gameCreationTimestamp, gameEndTimestamp, teamOnePUUID, teamTwoPUUID, participants)

					if err != nil {
						log.Fatalf("Insert failed: %v\n", err)
					}

					w.Header().Set("Content-Type", "application/x-protobuf")
					w.Write([]byte(fmt.Sprintf("%s results added to history for %s", unpackedRequest.MatchID, unpackedRequest.RiotName)))
				}()
				wg.Wait()

				// Key Exists: 			Deletes key
				// Key doesn't exist: 	Does nothing and just moves on
				activeMatches.Delete(match.MatchID)
				matchParticipantsMap.Delete(match.MatchID)
				// TODO: FIX THIS NIL POINTER DE-REFERENCE ERROR WHEN YOU WAKE UP IN THE MORNING
				// Its not that its experiencing a race condition since the delete is fairly atomically
				// Its being accessed at some point after this which is causing a nil pointer dereference
				// It might be deleting it before it is initialized or referencing it after delete

				// WORKS WITH DELAYED DELETION
				// NEED SOME SORT OF MECHANISM TO MAKE SURE DELETION IS NOT HAPPENING
				// TILL EVERY THREAD HAS PASSED THIS FUNCTION. LINES REFERENCING
				// RANDOMMATCH.MATCHID IS CAUSING PROBLEMS CAUSE ITS REFERENCING AFTER
				// DELETION CAUSE SOME THREADS ARE AHEAD OF OTHERS
				time.Sleep(5000 * time.Millisecond)
				// Prob gonna have to use wait groups
				matchDataMap.Delete(match.MatchID)
				return
			}

		}

		return
	}
}
