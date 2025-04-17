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
	"github.com/jackc/pgx/v5/pgxpool"
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

// func matchHeartBeat(activeMatches, matchCreationDates *sync.Map) {
// 	go func() {
// 		for {
// 			currentTime := time.Now().Unix()

// 			matchCreationDates.Range(func(key, value interface{}) bool {
// 				matchCreationDate, ok := value.(int64)
// 				if !ok {
// 					return true
// 				}
// 				// 86400 is the seconds in a day
// 				// Every day it checks for for left over artifacts and clears them out.
// 				// Should be faster for performances sake, lets say an hour or 2
// 				// Take average game time and multiply it by 2 and use that
// 				if currentTime-matchCreationDate > 3600 {
// 					matchCreationDates.Delete(key)
// 					activeMatches.Delete(key)
// 				}
// 				return true
// 			})

// 			time.Sleep(5 * time.Minute)
// 		}
// 	}()
// }

/*
- Unpacks connection requests - Difference from UnpackCreationRequest is
that Creation request contains a single matchID and all user PUUIDs.

- Connection comes in from the server
*/
func UnpackCreationRequest(w http.ResponseWriter, r *http.Request) (*gameServerProto.MatchCreation, error) {
	var unpackedRequest gameServerProto.MatchCreation
	err := UnpackRequest(w, r, &unpackedRequest)
	if err != nil {
		log.Printf("Could not unpack the payload: %d: %s", http.StatusBadRequest, err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
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

func UpdateProfile(dbPool *pgxpool.Pool, unpackedRequest *gameServerProto.MatchConnection, randomMatch *gameServerProto.MatchResult) error {

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
	err := dbPool.QueryRow(context.Background(),
		`SELECT rank, wins, losses FROM "summonerRankedInfo" WHERE puuid = $1`, unpackedRequest.ParticipantPUUID).
		Scan(&rank, &wins, &losses)
	if err == pgx.ErrNoRows {
		rank = unpackedRank
		wins = 0
		losses = 0
	} else if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("failed to fetch summoner rank info: %v", err)
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

	_, err = dbPool.Exec(context.Background(),
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
		return fmt.Errorf("insert failed: %v", err)
	}

	return nil
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

var dbPool *pgxpool.Pool

func initDB() {
	dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=10000"

	var err error
	dbPool, err = pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Failed to connect to DB pool: %v", err)
	}
}

// Endpoint that users will use to connect to the marked matches in the sync.Map
// Consumed by the /connectToMatch endpoint
func NewPlayerConnection(w http.ResponseWriter, r *http.Request, activeMatches *sync.Map, matchDataMap *sync.Map, matchParticipantsMap *sync.Map, databaseTransactionMutex *sync.Mutex, waitGroupMap *sync.Map) {
	unpackedRequest, err := UnpackConnectionRequest(w, r)
	if err != nil {
		log.Printf("Could not unpack the payload: %s\n", err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	// Loads data for relevant match that is in the marked sync.Map
	validateSyncStore, ok := activeMatches.Load(unpackedRequest.MatchID)
	if ok {
		match, valid := validateSyncStore.(*gameServerProto.MatchCreation)
		if !valid {
			log.Printf("Error: Type assertion failed")
			return
		}

		// Accesses the sync.map containing the different wait groups that are associated with the matches themselves
		// Associated with the global service level sync maps in main.go
		var wg *sync.WaitGroup
		if tempWaitGroup, exists := waitGroupMap.Load(unpackedRequest.MatchID); exists {
			wg = tempWaitGroup.(*sync.WaitGroup)
		} else {
			wg = &sync.WaitGroup{}
			waitGroupMap.Store(unpackedRequest.MatchID, wg)
		}

		// Loops through match PUUIDs in requested match ID to find out if you are in it
		for _, value := range match.ParticipantsPUUID {
			if value == unpackedRequest.ParticipantPUUID {

				wg.Add(1)

				// Main code block related to handling DB interactions
				// for the match history for each player
				go func() {
					defer wg.Done()
					err := ConnectPlayerToMatch(activeMatches, matchDataMap, match, matchParticipantsMap, unpackedRequest)
					if err != nil {
						log.Printf("Failed to connect player to match: %s\n", err)
						http.Error(w, fmt.Sprintf("Failed to connect player to match: %s", err), http.StatusBadRequest)
						return
					}

					var randomMatch *gameServerProto.MatchResult
					value, ok := matchDataMap.Load(match.MatchID)
					if !ok {
						log.Printf("No match data found for matchID: %s, %+v", match.MatchID, match) // returns nil for value
						http.Error(w, "Match data not found", http.StatusInternalServerError)
						return
					} else {
						randomMatch, ok = value.(*gameServerProto.MatchResult)
						if !ok {
							log.Printf("Failed to cast match data for matchID: %s", match.MatchID)
							http.Error(w, "Match data invalid", http.StatusInternalServerError)
							return
						}
					}

					participantValue, _ := matchParticipantsMap.Load(match.MatchID)
					var randomParticipants []*gameServerProto.Participant
					if participantValue != nil {
						randomParticipants = participantValue.([]*gameServerProto.Participant)
					}

					participantJsonData, err := json.Marshal(randomParticipants)
					if err != nil {
						log.Printf("Failed to convert to JSON: %s", err)
						http.Error(w, fmt.Sprintf("Failed to convert to JSON: %s", err), http.StatusBadRequest)
						return
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
					err = UpdateProfile(dbPool, unpackedRequest, randomMatch) // TODO: Profile not getting updated cause of No match data found for matchID erroring
					if err != nil {
						log.Printf("Could not update summoner data in database: %s\n", err)
						http.Error(w, fmt.Sprintf("Could not update summoner data in database: %s", err), http.StatusBadRequest)
						return
					}
					databaseTransactionMutex.Unlock()

					// Execute INSERT query
					_, err = dbPool.Exec(context.Background(),
						`INSERT INTO "matchHistory" 
						("matchID", "gameVer", "puuid", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants") 
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
						matchID, gameVer, puuid, gameDuration, gameCreationTimestamp, gameEndTimestamp, teamOnePUUID, teamTwoPUUID, participants)

					if err != nil {
						log.Printf("Match history insert failed: %s\n", err)
						http.Error(w, fmt.Sprintf("Match history insert failed: %s", err), http.StatusBadRequest)
						return
					}

					w.Header().Set("Content-Type", "application/x-protobuf")
					w.Write([]byte(fmt.Sprintf("%s results added to history for %s", unpackedRequest.MatchID, unpackedRequest.RiotName)))
				}()
				wg.Wait()

				// Deleting the associated data in the global sync maps
				//     containing things like match data, match wait group, and other stuff
				// If this is not deleted the service will just baloon in resource usage
				// 	    the more matches are run through it

				// TODO: I think that since some goroutines are finishing sooner than others or erroring out faster than outhers its causing premature deletes
				// matchParticipantsMap.Delete(match.MatchID)
				// activeMatches.Delete(match.MatchID)
				// waitGroupMap.Delete(match.MatchID)
				// matchDataMap.Delete(match.MatchID)

				// Success case
				return
			}

		}

		// Player not found in match
		return
	}
}
