package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noahpop77/Olympus/matchmaking_service/matchmakingProto"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

const (
	// matchSize is the number of players that make up a single match.
	matchSize = 10
	// rankWindow keeps the original "diff < 4 && diff > -4" behaviour.
	rankWindow = 4
	// matchmakerInterval is how often the background worker attempts to form
	// matches. It replaces the old per-player 10ms polling loop.
	matchmakerInterval = 250 * time.Millisecond
	// matchNotifyBuffer lets the worker hand a result to a waiting handler
	// without blocking if that handler is just about to exit.
	matchNotifyBuffer = 1
	// provisionConcurrency bounds how many game servers we spawn at once.
	provisionConcurrency = 16
	// dbTimeout bounds every database round trip.
	dbTimeout = 5 * time.Second
	// redisTimeout bounds every Redis round trip.
	redisTimeout = 2 * time.Second
)

// PartyResources is the per-request state for a player waiting in the queue.
//
// MatchChan is how the matchmaker delivers the result. Only the goroutine that
// owns this request writes to that request's http.ResponseWriter, which avoids
// the data races the previous implementation had.
type PartyResources struct {
	MatchChan chan []byte
	Player    *matchmakingProto.Players
	Rank      int
	QueuedAt  time.Time
	Ctx       context.Context
	cancelled atomic.Bool
}

// dead reports whether the player's request has already gone away.
func (p *PartyResources) dead() bool {
	return p.cancelled.Load() || (p.Ctx != nil && p.Ctx.Err() != nil)
}

// WithinRankRange checks if the rank difference is within a specified range
func WithinRankRange(myRank, teamRank int) bool {
	diff := myRank - teamRank
	return diff < rankWindow && diff > -rankWindow
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
	// UnpackRequest returns nil on any read/unmarshal failure. Guard before
	// dereferencing it, otherwise a malformed body panics the handler.
	if unpackedRequest == nil {
		return false
	}

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
	case unpackedRequest.PlayerRank < 0 || unpackedRequest.PlayerRank > 44:
		return false
	case unpackedRequest.PlayerRole != "Middle" && unpackedRequest.PlayerRole != "Top" && unpackedRequest.PlayerRole != "Jungle" && unpackedRequest.PlayerRole != "Bottom" && unpackedRequest.PlayerRole != "Support":
		return false

	default:
		return true
	}
}

func PrintBanner(port string) {
	fmt.Println(`=================================================
███╗   ███╗ █████╗ ████████╗ ██████╗██╗  ██╗     
████╗ ████║██╔══██╗╚══██╔══╝██╔════╝██║  ██║     
██╔████╔██║███████║   ██║   ██║     ███████║     
██║╚██╔╝██║██╔══██║   ██║   ██║     ██╔══██║     
██║ ╚═╝ ██║██║  ██║   ██║   ╚██████╗██║  ██║     
╚═╝     ╚═╝╚═╝  ╚═╝   ╚═╝    ╚═════╝╚═╝  ╚═╝     
███╗   ███╗ █████╗ ██╗  ██╗██╗███╗   ██╗ ██████╗ 
████╗ ████║██╔══██╗██║ ██╔╝██║████╗  ██║██╔════╝ 
██╔████╔██║███████║█████╔╝ ██║██╔██╗ ██║██║  ███╗
██║╚██╔╝██║██╔══██║██╔═██╗ ██║██║╚██╗██║██║   ██║
██║ ╚═╝ ██║██║  ██║██║  ██╗██║██║ ╚████║╚██████╔╝
╚═╝     ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝╚═╝  ╚═══╝ ╚═════╝`)
	fmt.Println("=================================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("=================================================")
}

func IsRunningInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// AddPartyToRedis handles party creation and Redis caching for the matchmaking request.
func AddPartyToRedis(w http.ResponseWriter, unpackedRequest *matchmakingProto.Players, myRank int, rdb *redis.Client, ctx context.Context) {
	err := rdb.HSet(ctx, unpackedRequest.PartyId,
		"PartyId", unpackedRequest.PartyId,
		"QueueType", unpackedRequest.QueueType,
		"PlayerPuuid", unpackedRequest.PlayerPuuid,
		"PlayerRiotName", unpackedRequest.PlayerRiotName,
		"PlayerRiotTagLine", unpackedRequest.PlayerRiotTagLine,
		"PlayerRank", myRank,
		"PlayerRole", unpackedRequest.PlayerRole).Err()

	if err != nil {
		log.Printf("could not set participant info: %v", err)
	}
}

// RemovePartyFromRedis deletes PartyIDs from Redis for players who cancel queue
// or whose match has already been provisioned.
func RemovePartyFromRedis(partyId string, rdb *redis.Client, ctx context.Context) {
	err := rdb.Del(ctx, partyId).Err()
	if err != nil {
		log.Printf("could not delete: %v", err)
	}
}

// provisionClient is shared so connections are reused across spawn requests.
var provisionClient = &http.Client{Timeout: 5 * time.Second}

func ProvisionGameServer(matchData []byte) bool {
	req, err := http.NewRequest(http.MethodPost, "http://game_server:8081/spawnMatch", bytes.NewReader(matchData))
	if err != nil {
		log.Printf("Failed to create spawn request: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := provisionClient.Do(req)
	if err != nil {
		log.Printf("Failed to provision game server: %v", err)
		return false
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode == http.StatusOK
}

// Matchmaker owns the queue of waiting parties and periodically groups
// compatible players into matches.
//
// The previous implementation had every queued player poll a global mutex
// every 10ms and perform a full Redis SCAN while holding that mutex. At 10k
// players that is ~1,000,000 lock acquisitions per second, each potentially
// walking the entire keyspace. Here a single worker snapshots an in-memory
// queue a few times per second, and all network I/O happens outside the lock.
type Matchmaker struct {
	mu      sync.Mutex
	waiting map[string]*PartyResources
	rdb     *redis.Client
	ctx     context.Context
	sem     chan struct{}
}

func NewMatchmaker(ctx context.Context, rdb *redis.Client) *Matchmaker {
	return &Matchmaker{
		waiting: make(map[string]*PartyResources),
		rdb:     rdb,
		ctx:     ctx,
		sem:     make(chan struct{}, provisionConcurrency),
	}
}

// Start launches the background matchmaking loop.
func (m *Matchmaker) Start() {
	go m.run()
}

func (m *Matchmaker) run() {
	ticker := time.NewTicker(matchmakerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.formMatches()
		}
	}
}

// Enqueue adds a waiting party to the pool.
func (m *Matchmaker) Enqueue(party *PartyResources) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if party.dead() {
		return
	}
	m.waiting[party.Player.PartyId] = party
}

// Remove drops a party from the pool. It is safe to call for parties that the
// worker has already claimed.
func (m *Matchmaker) Remove(partyID string) {
	m.mu.Lock()
	delete(m.waiting, partyID)
	m.mu.Unlock()
}

// formMatches builds as many valid groups as it can from the current pool.
// It holds the lock only long enough to snapshot and claim parties; provisioning
// the game server and notifying players happen outside the lock.
func (m *Matchmaker) formMatches() {
	m.mu.Lock()

	if len(m.waiting) < matchSize {
		m.mu.Unlock()
		return
	}

	pool := make([]*PartyResources, 0, len(m.waiting))
	for _, party := range m.waiting {
		// Drop requests whose client has already disconnected instead of
		// filling a match with players who will never connect.
		if party.dead() {
			delete(m.waiting, party.Player.PartyId)
			continue
		}
		pool = append(pool, party)
	}
	sort.Slice(pool, func(i, j int) bool {
		return pool[i].Rank < pool[j].Rank
	})

	used := make(map[string]struct{}, len(pool))
	groups := make([][]*PartyResources, 0)

	for i := 0; i < len(pool); i++ {
		if _, ok := used[pool[i].Player.PartyId]; ok {
			continue
		}

		group := make([]*PartyResources, 0, matchSize)
		group = append(group, pool[i])

		for j := i + 1; j < len(pool) && len(group) < matchSize; j++ {
			if _, ok := used[pool[j].Player.PartyId]; ok {
				continue
			}
			// The pool is sorted by rank, so once a player is out of range
			// every later player is too.
			if !WithinRankRange(group[0].Rank, pool[j].Rank) {
				break
			}
			group = append(group, pool[j])
		}

		if len(group) != matchSize {
			continue
		}
		for _, party := range group {
			used[party.Player.PartyId] = struct{}{}
		}
		groups = append(groups, group)
	}

	// Claim the groups while still holding the lock so no two workers can
	// hand out the same player.
	claimed := make([][]*PartyResources, 0, len(groups))
	for _, group := range groups {
		stillWaiting := true
		for _, party := range group {
			if _, ok := m.waiting[party.Player.PartyId]; !ok {
				stillWaiting = false
				break
			}
		}
		if !stillWaiting {
			continue
		}
		for _, party := range group {
			delete(m.waiting, party.Player.PartyId)
		}
		claimed = append(claimed, group)
	}

	m.mu.Unlock()

	for _, group := range claimed {
		group := group
		m.sem <- struct{}{}
		go func() {
			defer func() { <-m.sem }()
			m.startMatch(group)
		}()
	}
}

// startMatch provisions a game server for the group and then hands the match
// payload to each player's own handler.
func (m *Matchmaker) startMatch(group []*PartyResources) {
	response := &matchmakingProto.MatchResponse{
		MatchID:           generateMatchID(),
		ParticipantsPUUID: make([]string, 0, matchSize),
	}
	for _, party := range group {
		response.ParticipantsPUUID = append(response.ParticipantsPUUID, party.Player.PlayerPuuid)
	}

	data, err := proto.Marshal(response)
	if err != nil {
		log.Printf("Failed to marshal match response: %v", err)
		m.requeue(group)
		return
	}

	if !ProvisionGameServer(data) {
		log.Printf("Failed to provision game server for match %s; re-queueing %d players", response.MatchID, len(group))
		m.requeue(group)
		return
	}

	// Notify each waiting handler. Redis cleanup happens in that handler's
	// deferred teardown, so there is no need for 10 sequential round trips here.
	for _, party := range group {
		select {
		case party.MatchChan <- data:
		default:
			// The handler has already gone away; nothing to notify.
		}
	}
}

// requeue puts parties back when provisioning fails, unless their handler has
// already disconnected.
func (m *Matchmaker) requeue(group []*PartyResources) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, party := range group {
		if party.dead() {
			continue
		}
		m.waiting[party.Player.PartyId] = party
	}
}

func generateMatchID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	// The package-level math/rand functions are safe for concurrent use and
	// automatically seeded. The previous per-call rand.NewSource(time.Now())
	// could produce identical IDs for matches formed in the same nanosecond.
	id := make([]byte, 10)
	for i := range id {
		id[i] = charset[rand.Intn(len(charset))]
	}
	return "MATCH_" + string(id)
}

// MatchFinder waits for the matchmaker to deliver a result. Only this handler
// goroutine ever touches w, which is what net/http requires.
func MatchFinder(w http.ResponseWriter, party *PartyResources, requester *http.Request, matchCtx context.Context) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("Streaming not supported - %d", http.StatusInternalServerError)
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	select {
	case data := <-party.MatchChan:
		if len(data) == 0 {
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		if _, err := w.Write(data); err != nil {
			log.Printf("Failed to write match response: %v", err)
			return
		}
		flusher.Flush()

	case <-requester.Context().Done():
		return

	case <-matchCtx.Done():
		return
	}
}

var dbPool *pgxpool.Pool

func initDB() {
	// Keep the pool well below Postgres max_connections (see dbScripts/init.sql
	// and docker-compose.yaml). The old value of 10000 meant pgxpool would try
	// to open far more connections than Postgres would ever accept.
	dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=50&pool_min_conns=5&pool_max_conn_idle_time=1m&pool_max_conn_lifetime=30m"

	var err error
	dbPool, err = pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Failed to connect to DB pool: %v", err)
	}
}

func QueueUp(w http.ResponseWriter, r *http.Request, mm *Matchmaker, rdb *redis.Client, baseCtx context.Context) {
	activeConnections.Inc()
	defer activeConnections.Dec()

	unpackedRequest := UnpackRequest(w, r)
	if !UnpackedRequestValidation(unpackedRequest) {
		log.Printf("Missing required data in payload - %d", http.StatusBadRequest)
		http.Error(w, "Missing required data in payload", http.StatusBadRequest)
		return
	}

	// Validates with the summonerRankedInfo database and uses that value for the
	// user if it exists; otherwise fall back to the value the client provided.
	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbTimeout)
	defer dbCancel()

	var myRank int
	err := dbPool.QueryRow(dbCtx,
		`SELECT rank FROM "summonerRankedInfo" WHERE puuid = $1`, unpackedRequest.PlayerPuuid).
		Scan(&myRank)
	if err == pgx.ErrNoRows {
		myRank = int(unpackedRequest.PlayerRank)
	} else if err != nil {
		log.Printf("Failed to fetch summoner rank info: %v\n", err)
		http.Error(w, "Failed to fetch summoner rank info", http.StatusServiceUnavailable)
		return
	}

	redisCtx, redisCancel := context.WithTimeout(r.Context(), redisTimeout)
	AddPartyToRedis(w, unpackedRequest, myRank, rdb, redisCtx)
	redisCancel()

	party := &PartyResources{
		MatchChan: make(chan []byte, matchNotifyBuffer),
		Player:    unpackedRequest,
		Rank:      myRank,
		QueuedAt:  time.Now(),
		Ctx:       r.Context(),
	}
	mm.Enqueue(party)

	defer func() {
		party.cancelled.Store(true)
		mm.Remove(unpackedRequest.PartyId)

		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), redisTimeout)
		RemovePartyFromRedis(unpackedRequest.PartyId, rdb, cleanupCtx)
		cleanupCancel()
	}()

	MatchFinder(w, party, r, baseCtx)
}
