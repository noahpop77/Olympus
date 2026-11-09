package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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

const (
	// matchSize is the number of players in a single match.
	matchSize = 10
	// matchConnectTimeout bounds how long a connected player waits for the
	// other participants to show up. Without this the old loops spun forever
	// whenever a match never filled up, leaking a goroutine per player.
	matchConnectTimeout = 2 * time.Minute
	// A match's simulated game length is derived from its match ID, in
	// [minGameDuration, maxGameDuration]. The client derives the same value
	// from the same ID, so both sides agree without an extra round trip, and
	// matches do not all start and end in lockstep.
	minGameDuration = 200 * time.Second
	maxGameDuration = 400 * time.Second
	// matchCleanupGrace is added to the watchdog once a match is full so the
	// maps survive the whole game plus the database writes that follow it.
	matchCleanupGrace = 2 * time.Minute
	// dbOpTimeout bounds every database round trip.
	dbOpTimeout = 5 * time.Second
	// participantPoll is how often a player checks whether all participants
	// have been generated yet.
	participantPoll = 25 * time.Millisecond
	// matchRetentionSeconds is how long a match may stay in the global maps
	// before the janitor removes it.
	matchRetentionSeconds = 3600
)

// matchGameDuration returns a stable per-match game length in
// [minGameDuration, maxGameDuration]. It is a pure function of the match ID so
// the game server and the client compute the same duration independently; no
// protocol change or extra request is needed.
//
// Match IDs are "MATCH_" plus ten random base-36 characters, so the FNV hash is
// uniformly distributed across the range and different matches get different
// lengths.
func matchGameDuration(matchID string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(matchID))

	spanSeconds := int64((maxGameDuration - minGameDuration) / time.Second)
	offsetSeconds := int64(h.Sum64() % uint64(spanSeconds+1))
	return minGameDuration + time.Duration(offsetSeconds)*time.Second
}

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

// matchHeartBeat is a janitor that removes matches which were created but never
// fully consumed. The per-match tracker already cleans up normal matches; this
// is the safety net for anything that slips through (for example a match whose
// players all disconnect before the tracker exists).
func matchHeartBeat(activeMatches, matchDataMap, matchParticipantsMap, matchCreationDates, waitGroupMap *sync.Map) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			currentTime := time.Now().Unix()

			matchCreationDates.Range(func(key, value any) bool {
				created, ok := value.(int64)
				if !ok || currentTime-created > matchRetentionSeconds {
					matchCreationDates.Delete(key)
					activeMatches.Delete(key)
					matchDataMap.Delete(key)
					matchParticipantsMap.Delete(key)
					waitGroupMap.Delete(key)
				}
				return true
			})
		}
	}()
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

	unpackedRank, rankErr := strconv.Atoi(unpackedRequest.Rank)
	if rankErr != nil {
		log.Printf("String conversion error: %v", rankErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbOpTimeout)
	defer cancel()

	var rank, wins, losses int
	err := dbPool.QueryRow(ctx,
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

	_, err = dbPool.Exec(ctx,
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

// ConnectPlayerToMatch waits (with a deadline) until every participant in the
// match has generated its game data, then simulates the game duration.
//
// The old implementation looped forever with 250ms sleeps and no deadline: a
// single player that never connected left the other nine goroutines spinning
// for the lifetime of the process.
func ConnectPlayerToMatch(activeMatches *sync.Map, matchDataMap *sync.Map, match *gameServerProto.MatchCreation, matchParticipantsMap *sync.Map, unpackedRequest *gameServerProto.MatchConnection) error {
	if len(match.ParticipantsPUUID) != matchSize {
		return fmt.Errorf("match %s has %d participants, expected %d", match.MatchID, len(match.ParticipantsPUUID), matchSize)
	}

	// Each player handler contributes exactly one participant entry.
	generateGameData(match, matchParticipantsMap, matchDataMap, unpackedRequest)

	deadline := time.Now().Add(matchConnectTimeout)
	for {
		participantValue, ok := matchParticipantsMap.Load(match.MatchID)
		if ok && participantValue != nil {
			randomParticipants, valid := participantValue.([]*gameServerProto.Participant)
			if !valid {
				return fmt.Errorf("invalid participant data for match ID %s", match.MatchID)
			}
			if len(randomParticipants) >= matchSize {
				// Simulate this match's game duration. The client derives the
				// same value from the match ID before it asks for the result.
				time.Sleep(matchGameDuration(match.MatchID))
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %d participants in match %s", matchSize, match.MatchID)
		}
		time.Sleep(participantPoll)
	}
}

var dbPool *pgxpool.Pool

func initDB() {
	dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=50&pool_min_conns=5&pool_max_conn_idle_time=1m&pool_max_conn_lifetime=30m"

	var err error
	dbPool, err = pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Failed to connect to DB pool: %v", err)
	}
}

// Endpoint that users will use to connect to the marked matches in the sync.Map
// Consumed by the /connectToMatch endpoint
func NewPlayerConnectionBackup(w http.ResponseWriter, r *http.Request, activeMatches *sync.Map, matchDataMap *sync.Map, matchParticipantsMap *sync.Map, databaseTransactionMutex *sync.Mutex, waitGroupMap *sync.Map) {
	unpackedRequest, err := UnpackConnectionRequest(w, r)
	if err != nil {
		log.Printf("Could not unpack the payload: %s\n", err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	// Loads data for relevant match that is in the marked sync.Map
	validateSyncStore, ok := activeMatches.Load(unpackedRequest.MatchID)
	// log.Printf("%v", validateSyncStore)

	/*
		log.Printf("%v", validateSyncStore)
		matchID:"MATCH_94AUBSDPIP"  participantsPUUID:"aysgUr2TEpUzxdtQS2zrTXNSPkx4RHoku7p2YjxHRawtC8USc75I219LzOSPATdV"  participantsPUUID:"QvJg2HFasDt4VEDJ2h8HMTsusiphYzk0EAfgNQtfen8rrIBQVZCZ4nxZCzyg5l1U"  participantsPUUID:"VOOu8B72wIVQ0edj58wlRjTwDuyy1EVkZK9GVkVQnGYtjPiNrjczRaobd8Zl3TSY"  participantsPUUID:"28MmZd3DbdAmqodDro7iUythnEwaJZgBeWZM76LZRcfJQwL7El2MDWv1LyBz3Zoj"  participantsPUUID:"Fw91wP75WR60J2uxu5wSFByZAOLCHMbK3217rR32XjJGzGF1Ba7EVdTtGFcgtc29"  participantsPUUID:"rcdFEpMnbGodPVfCoH91cwotmBLnykNBtbx3EucLRjEOndlutIw28Tz6pYOo54Qz"  participantsPUUID:"RMtiTWWBzVMcvzS4EACkFwMvuPz7FAfLeOcENSzSfW3wdDXdIzegLE8GklFy8UOr"  participantsPUUID:"pO9Uh725yPlViGYGqipPllBzDsKkcVsojEtLyluEL3RK7IQa8t3uUv2YYzTGnHfV"  participantsPUUID:"frnH3qRBTCEoo4ETRR84ZWmomVJ0uIgCeO8sYDWRdDXJu56da9PsbKZXUosMYOwR"  participantsPUUID:"61I2nCiKV9CEcjCdKt4rG9YotJ1rGhF2ccgq3qWkaeDWD9hheAq5hyFXKdZY2yW5"

	*/

	if ok {
		match, valid := validateSyncStore.(*gameServerProto.MatchCreation)
		if !valid {
			log.Printf("Error: Type assertion failed")
			return
		}

		// log.Printf("match data: %v", match)

		/*
			log.Printf("match data: %v", match)
			match data: matchID:"MATCH_94AUBSDPIP"  participantsPUUID:"aysgUr2TEpUzxdtQS2zrTXNSPkx4RHoku7p2YjxHRawtC8USc75I219LzOSPATdV"  participantsPUUID:"QvJg2HFasDt4VEDJ2h8HMTsusiphYzk0EAfgNQtfen8rrIBQVZCZ4nxZCzyg5l1U"  participantsPUUID:"VOOu8B72wIVQ0edj58wlRjTwDuyy1EVkZK9GVkVQnGYtjPiNrjczRaobd8Zl3TSY"  participantsPUUID:"28MmZd3DbdAmqodDro7iUythnEwaJZgBeWZM76LZRcfJQwL7El2MDWv1LyBz3Zoj"  participantsPUUID:"Fw91wP75WR60J2uxu5wSFByZAOLCHMbK3217rR32XjJGzGF1Ba7EVdTtGFcgtc29"  participantsPUUID:"rcdFEpMnbGodPVfCoH91cwotmBLnykNBtbx3EucLRjEOndlutIw28Tz6pYOo54Qz"  participantsPUUID:"RMtiTWWBzVMcvzS4EACkFwMvuPz7FAfLeOcENSzSfW3wdDXdIzegLE8GklFy8UOr"  participantsPUUID:"pO9Uh725yPlViGYGqipPllBzDsKkcVsojEtLyluEL3RK7IQa8t3uUv2YYzTGnHfV"  participantsPUUID:"frnH3qRBTCEoo4ETRR84ZWmomVJ0uIgCeO8sYDWRdDXJu56da9PsbKZXUosMYOwR"  participantsPUUID:"61I2nCiKV9CEcjCdKt4rG9YotJ1rGhF2ccgq3qWkaeDWD9hheAq5hyFXKdZY2yW5"
		*/

		// Accesses the sync.map containing the different wait groups that are associated with the matches themselves
		// Associated with the global service level sync maps in main.go
		// var wg *sync.WaitGroup
		// tempWaitGroup, exists := waitGroupMap.Load(unpackedRequest.MatchID)
		// if exists {
		// 	wg = tempWaitGroup.(*sync.WaitGroup)
		// } else {
		// 	wg = &sync.WaitGroup{}
		// 	waitGroupMap.Store(unpackedRequest.MatchID, wg)
		// }

		// === SIMPLE & WORKING WAITGROUP FIX ===
		type matchTracker struct {
			wg    sync.WaitGroup
			count int
			mu    sync.Mutex
		}

		// Load existing tracker or create new one
		value, _ := waitGroupMap.LoadOrStore(unpackedRequest.MatchID, &matchTracker{})
		tracker := value.(*matchTracker)

		tracker.mu.Lock()
		tracker.count++
		isTenthPlayer := (tracker.count == 10) // change to len(match.ParticipantsPUUID) if you want dynamic size
		tracker.mu.Unlock()

		tracker.wg.Add(1)

		// Loops through match PUUIDs in requested match ID to find out if you are in it
		for _, value := range match.ParticipantsPUUID {
			if value == unpackedRequest.ParticipantPUUID {

				//wg.Add(1)

				// log.Printf("Start count %v", tracker.count)

				// Main code block related to handling DB interactions
				// for the match history for each player
				go func() {
					//defer wg.Done()
					defer func() {
						tracker.wg.Done()
					}()

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
					databaseTransactionMutex.Unlock()

					w.Header().Set("Content-Type", "application/x-protobuf")
					w.Write([]byte(fmt.Sprintf("%s results added to history for %s", unpackedRequest.MatchID, unpackedRequest.RiotName)))

				}()

				log.Printf("end count %v", tracker.count)
				// Only the 10th player waits and cleans up
				if isTenthPlayer {
					tracker.wg.Wait()

					// Cleanup global maps — safe, only happens once
					activeMatches.Delete(unpackedRequest.MatchID)
					matchDataMap.Delete(unpackedRequest.MatchID)
					matchParticipantsMap.Delete(unpackedRequest.MatchID)
					waitGroupMap.Delete(unpackedRequest.MatchID)
					return
				}

				//wg.Wait()

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

// matchTracker coordinates the ten concurrent /connectToMatch handlers that
// belong to a single match.
//
// arrived is incremented under mu *after* wg.Add(1), so by the time the final
// participant is seen every handler has already registered with the WaitGroup.
// That ordering removes the race in the old implementation where the "tenth"
// player could call wg.Wait() before the ninth player had called wg.Add(1).
type matchTracker struct {
	mu           sync.Mutex
	expected     int
	arrived      int
	seen         map[string]struct{}
	done         chan struct{}
	doneOnce     sync.Once
	wg           sync.WaitGroup
	cleanOnce    sync.Once
	timer        *time.Timer
	gameDuration time.Duration
}

func newMatchTracker(expected int, cleanup func(), gameDuration time.Duration) *matchTracker {
	t := &matchTracker{
		expected:     expected,
		done:         make(chan struct{}),
		seen:         make(map[string]struct{}, expected),
		gameDuration: gameDuration,
	}
	// Watchdog: if fewer than expected players ever connect, release the maps
	// instead of leaking them for the lifetime of the process.
	t.timer = time.AfterFunc(matchConnectTimeout, func() {
		t.cleanOnce.Do(cleanup)
	})
	return t
}

// arrive registers a handler with the tracker and reports whether this was the
// final expected participant. A connection beyond the expected count (or a
// repeated PUUID) is rejected instead of calling wg.Add, which would race the
// cleanup wg.Wait.
func (t *matchTracker) arrive(puuid string) (isLast bool, duplicate bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, seen := t.seen[puuid]; seen {
		return false, true
	}
	if t.arrived >= t.expected {
		return false, true
	}

	t.seen[puuid] = struct{}{}
	t.wg.Add(1)
	t.arrived++
	if t.arrived == t.expected {
		// The match is full, so the connect window is over. Extend the
		// watchdog to cover this match's simulated game and the database
		// writes instead of deleting the match two minutes after it started.
		t.timer.Reset(t.gameDuration + matchCleanupGrace)
		t.doneOnce.Do(func() { close(t.done) })
		return true, false
	}
	return false, false
}

// Endpoint that users will use to connect to the marked matches in the sync.Map
// Consumed by the /connectToMatch endpoint
func NewPlayerConnection(w http.ResponseWriter, r *http.Request, activeMatches *sync.Map, matchDataMap *sync.Map, matchParticipantsMap *sync.Map, matchCreationDates *sync.Map, waitGroupMap *sync.Map) {
	unpackedRequest, err := UnpackConnectionRequest(w, r)
	if err != nil {
		log.Printf("Could not unpack the payload: %s\n", err)
		http.Error(w, "Could not unpack the payload", http.StatusBadRequest)
		return
	}

	matchID := unpackedRequest.MatchID

	// Loads data for relevant match that is in the marked sync.Map
	validateSyncStore, ok := activeMatches.Load(matchID)
	if !ok {
		log.Printf("No active match for matchID: %s", matchID)
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	match, valid := validateSyncStore.(*gameServerProto.MatchCreation)
	if !valid {
		log.Printf("Error: Type assertion failed")
		http.Error(w, "Invalid match", http.StatusInternalServerError)
		return
	}

	// Change to check
	var isParticipant bool
	for _, p := range match.ParticipantsPUUID {
		if p == unpackedRequest.ParticipantPUUID {
			isParticipant = true
			break
		}
	}
	if !isParticipant {
		log.Printf("Player %s not in match %s participants list — rejecting",
			unpackedRequest.ParticipantPUUID, matchID)
		http.Error(w, "Not in match", http.StatusForbidden)
		return
	}

	// cleanup removes every global artifact for this match. cleanOnce makes it
	// safe to call from both the normal completion path and the watchdog.
	cleanup := func() {
		activeMatches.Delete(matchID)
		matchDataMap.Delete(matchID)
		matchParticipantsMap.Delete(matchID)
		matchCreationDates.Delete(matchID)
		waitGroupMap.Delete(matchID)
		releaseParticipantLock(matchID)
	}

	expected := len(match.ParticipantsPUUID)
	if expected == 0 {
		expected = matchSize
	}

	var tracker *matchTracker
	if value, loaded := waitGroupMap.Load(matchID); loaded {
		tracker, _ = value.(*matchTracker)
	}
	if tracker == nil {
		candidate := newMatchTracker(expected, cleanup, matchGameDuration(matchID))
		value, loaded := waitGroupMap.LoadOrStore(matchID, candidate)
		if loaded {
			// Another handler won the race; drop this candidate's watchdog.
			candidate.timer.Stop()
		}
		tracker, ok = value.(*matchTracker)
		if !ok {
			log.Printf("Invalid match tracker for matchID: %s", matchID)
			http.Error(w, "Invalid match state", http.StatusInternalServerError)
			return
		}
	}

	isLast, duplicate := tracker.arrive(unpackedRequest.ParticipantPUUID)
	if duplicate {
		log.Printf("Duplicate connection for match %s from %s — rejecting", matchID, unpackedRequest.ParticipantPUUID)
		http.Error(w, "Duplicate connection", http.StatusConflict)
		return
	}

	// Count the player for the whole match lifetime, not just for the HTTP
	// handler. The handler returns as soon as the player is registered, so
	// incrementing the gauge in main.go made "Players In Game" read ~0.
	activeConnections.Inc()
	go func() {
		defer activeConnections.Dec()
		defer tracker.wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic while recording match %s for %s: %v", matchID, unpackedRequest.ParticipantPUUID, rec)
			}
		}()

		if err := ConnectPlayerToMatch(activeMatches, matchDataMap, match, matchParticipantsMap, unpackedRequest); err != nil {
			log.Printf("Failed to connect player to match: %s\n", err)
			return
		}

		value, ok := matchDataMap.Load(matchID)
		if !ok {
			log.Printf("No match data found for matchID: %s", matchID)
			return
		}
		randomMatch, ok := value.(*gameServerProto.MatchResult)
		if !ok {
			log.Printf("Failed to cast match data for matchID: %s", matchID)
			return
		}

		participantValue, _ := matchParticipantsMap.Load(matchID)
		var randomParticipants []*gameServerProto.Participant
		if participantValue != nil {
			var participantValid bool
			randomParticipants, participantValid = participantValue.([]*gameServerProto.Participant)
			if !participantValid {
				log.Printf("Invalid participant data for matchID: %s", matchID)
				return
			}
		}

		participantJsonData, err := json.Marshal(randomParticipants)
		if err != nil {
			log.Printf("Failed to convert to JSON: %s", err)
			return
		}

		// Define match data
		puuid := unpackedRequest.ParticipantPUUID
		gameDuration := randomMatch.GameDuration
		gameCreationTimestamp := randomMatch.GameStartTime
		gameEndTimestamp := randomMatch.GameEndTime
		teamOnePUUID := randomMatch.TeamOnePUUID
		teamTwoPUUID := randomMatch.TeamTwoPUUID
		participants := participantJsonData

		if err := UpdateProfile(dbPool, unpackedRequest, randomMatch); err != nil {
			log.Printf("Could not update summoner data in database: %s\n", err)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), dbOpTimeout)
		defer cancel()

		// Execute INSERT query
		_, err = dbPool.Exec(ctx,
			`INSERT INTO "matchHistory"
			("matchID", "gameVer", "puuid", "gameDuration", "gameCreationTimestamp", "gameEndTimestamp", "teamOnePUUID", "teamTwoPUUID", "participants")
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			randomMatch.MatchID, randomMatch.GameVersion, puuid, gameDuration, gameCreationTimestamp, gameEndTimestamp, teamOnePUUID, teamTwoPUUID, participants)

		if err != nil {
			log.Printf("Match history insert failed: %s\n", err)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write([]byte(fmt.Sprintf("%s Processing for %s", matchID, unpackedRequest.RiotName)))

	// The handler that completed the match waits for every player's DB work to
	// finish and then releases the global maps exactly once. Every handler has
	// already registered with the WaitGroup by this point (see arrive).
	if isLast {
		go func() {
			tracker.wg.Wait()
			tracker.timer.Stop()
			tracker.cleanOnce.Do(cleanup)
		}()
	}
}
