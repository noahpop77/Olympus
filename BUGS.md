# Olympus — Dropped Connections at 10k Users

**A complete post-mortem of the bugs that caused connections to drop, users to disappear,
and the load test to collapse around 10,000 simulated concurrent players.**

| | |
|---|---|
| **Repos** | `olympus` (server) + `protobufClient` (load generator) |
| **Symptom** | `resource temporarily unavailable`, `connection reset by peer`, lost active users, stalled matches |
| **Target** | 10,000 concurrent simulated players for several minutes |
| **Result** | 31,601 matches / 316,010 history rows in 180s, zero errors, flat resource usage |
| **Status** | Fixed and validated; changes are uncommitted |

> This document describes the code **before** the fix (the `-` side of each diff) and
> **after** it (the `+` side). Every snippet below is taken from the actual working tree.

---

## Contents

1. [TL;DR](#1-tldr)
2. [The symptom](#2-the-symptom)
3. [How a request flows](#3-how-a-request-flows)
4. [The failure cascade at 10k users](#4-the-failure-cascade-at-10k-users)
5. [Bug index](#5-bug-index)
6. [Client bugs (C1–C11)](#6-client-bugs-protobufclient)
7. [Matchmaking bugs (M1–M17)](#7-matchmaking-bugs-matchmaking_service)
8. [Game server bugs (G1–G20)](#8-game-server-bugs-game_server_service)
9. [Platform bugs (P1–P9)](#9-platform-bugs-platform_service)
10. [Infrastructure bugs (I1–I4)](#10-infrastructure-bugs)
11. [Why the fixes work together](#11-why-the-fixes-work-together)
12. [Validation](#12-validation)
13. [How to reproduce](#13-how-to-reproduce)
14. [What was intentionally not changed](#14-what-was-intentionally-not-changed)
15. [Remaining limitations](#15-remaining-limitations)
16. [File change index](#16-file-change-index)

---

## 1. TL;DR

There was no single bug. There were **five independent failure modes that reinforced each
other**:

1. **The load generator leaked a file descriptor on every request** because it deferred
   `Body.Close()` inside an infinite loop. It ran out of FDs and started failing with
   `connect: resource temporarily unavailable`.
2. **The load generator also parsed responses incorrectly** — it unmarshalled arbitrary TCP
   chunks instead of the full body — so players silently died on partial reads.
3. **Matchmaking was O(n²)**: every player polled every 10 ms, took a global mutex, and
   performed a full Redis `SCAN` of the keyspace while holding it. At 10k players that is
   ~1,000,000 lock acquisitions per second.
4. **The game server serialized every database write behind one global mutex** and had
   **unbounded polling loops** that leaked a goroutine per player whenever a match never
   filled.
5. **All three services asked Postgres for 10,000 connections each** (`pgxpool`
   `pool_max_conns=10000` × 3) while Postgres was configured for **100 total**. Every
   database call eventually failed with `too many clients`.

Each one alone degrades the system. Together they produce a cascade where the client loses
FDs, the servers lose goroutines, Postgres refuses connections, and the whole test
collapses.

---

## 2. The symptom

The load test ran cleanly at a few hundred users and fell apart as it approached ~10k:

```
Failed to send request 8: Post "http://localhost:8082/riotProfile":
    dial tcp [::1]:8082: connect: resource temporarily unavailable

Failed to send request 6: Post "http://localhost:8082/matchHistory":
    read tcp [::1]:57101->[::1]:8082: read: connection reset by peer

Failed to unmarshal data: proto: cannot parse invalid wire-format data

platform_server | No match found for PUUID: ...
game_server     | end count 6
```

Three distinct things are visible in those lines:

- **`resource temporarily unavailable`** — the client process hit its file-descriptor limit.
- **`connection reset by peer`** — the server closed a connection the client still expected
  to use (timeout, panic, or overload).
- **`cannot parse invalid wire-format data`** — the client was reading a protobuf message in
  pieces and trying to decode each piece separately.

The "active user count" (the `counter` in the old client) only ever went **down**, because
any error caused a player goroutine to `return` and never come back.

---

## 3. How a request flows

```
                        ┌──────────────────────────────┐
                        │        protobufClient        │
                        │   N goroutines = N players   │
                        └──────────────┬───────────────┘
                                       │ POST /queueUp (long-lived)
                                       ▼
                        ┌──────────────────────────────┐
                        │    matchmaking_service :8080 │
                        │  queue → group 10 players    │
                        └──────┬───────────────┬───────┘
                               │               │
                     POST /spawnMatch     match payload
                               ▼               ▼
                        ┌──────────────────────────────┐
                        │    game_server_service :8081 │
                        │  /spawnMatch, /connectToMatch│
                        │  write matchHistory + profile│
                        └──────────────┬───────────────┘
                                       │
                                       ▼
                        ┌──────────────────────────────┐
                        │        PostgreSQL            │
                        └──────────────┬───────────────┘
                                       ▲
                                       │ GET /matchHistory, /riotProfile
                        ┌──────────────┴───────────────┐
                        │    platform_service :8082    │
                        └──────────────────────────────┘
```

A single simulated player does this forever:

1. `POST /queueUp` and **hold the connection open** until a match is formed.
2. Receive a `MatchResponse` containing the match ID.
3. `POST /connectToMatch` to the game server.
4. Sleep 5–6 seconds (simulated game).
5. `POST /matchHistory`.
6. `POST /riotProfile`.
7. Repeat.

At 10k players, steps 1–3 are concurrently live for thousands of connections at once.

---

## 4. The failure cascade at 10k users

This is the order in which the system actually broke down.

### Stage 1 — Client starts and immediately over-subscribes

- 10,000 goroutines start at the same instant (no stagger).
- Each request creates a **brand-new `http.Client`**, which creates a **brand-new
  `http.Transport`**, which opens a **brand-new TCP connection**. Nothing is reused.
- Each response body is closed with `defer` **inside an infinite loop**, so the close never
  runs.

Result: file descriptors climb monotonically, ephemeral ports churn, and the process heads
toward `EMFILE` (`resource temporarily unavailable`).

### Stage 2 — Client mis-parses responses and players die

- The response is read into a 1 MB buffer with `Body.Read`.
- Each chunk is passed to `proto.Unmarshal` independently.
- A protobuf message that spans two TCP segments fails to decode.
- The goroutine `return`s. That player is gone for the rest of the test.

Result: active users silently shrink even before the server is stressed.

### Stage 3 — Matchmaking becomes a lock convoy

- Every queued player runs `MatchmakingSelection` every **10 ms**.
- Each call takes a **global mutex** and holds it while doing a **full Redis `SCAN`**.
- 10k players → ~1,000,000 lock acquisitions/sec, each potentially walking the entire
  keyspace.

Result: `/queueUp` handlers serialize behind one another; latency explodes; clients time out
and reset.

### Stage 4 — Postgres refuses connections

- `pgxpool` is configured with `pool_max_conns=10000` in **all three** services.
- Postgres is configured with `max_connections=100`.
- Under load the pools try to open far more connections than Postgres will accept.

Result: `FATAL: sorry, too many clients already`. Every database operation fails, so
matchmaking, match recording, and profile/history reads all error out.

### Stage 5 — Game server leaks goroutines and serializes writes

- Every player's DB work takes a **single global `databaseTransactionMutex`**.
- `ConnectPlayerToMatch` polls forever (250 ms) if a match never reaches 10 participants.
- Participants are appended with an unlocked **Load → append → Store**, so concurrent
  appends can be lost, which *guarantees* some matches never reach 10 — feeding the
  infinite loops.

Result: goroutines and memory grow without bound; writes queue behind one mutex; matches
never complete; the service degrades until it is killed.

### Stage 6 — Collapse

Client FDs exhausted → servers holding dead connections → Postgres rejecting new
connections → matchmaking serialized → game server leaking. The test does not recover; it
just loses users faster and faster.

---

## 5. Bug index

| ID | Severity | File | Bug | Effect |
|---|---|---|---|---|
| C1 | **Critical** | `protobufClient/profiles.go` | `defer Body.Close()` inside infinite loop | FD leak → `resource temporarily unavailable` |
| C2 | **Critical** | `protobufClient/profiles.go` | New `http.Client` per request | No connection reuse, port/goroutine churn |
| C3 | **Critical** | `protobufClient/profiles.go` | Chunked `Read` + `Unmarshal` per chunk | Partial protobuf decode failures, players die |
| C4 | **Critical** | `protobufClient/profiles.go` | `log.Fatalf` in player goroutines | One error kills the entire 10k-user test |
| C5 | High | `protobufClient/profiles.go` | Shared `*rand.Rand` across goroutines | Data race, corrupted state |
| C6 | High | `protobufClient/randomizedMatch.go` | `RandomIntInRange` calls `log.Fatalf` | Whole process exits on bad range |
| C7 | High | `protobufClient/profiles.go` | Retry loop `continue` with no backoff | Tight spin, hammers server/CPU |
| C8 | High | `protobufClient/profiles.go` | No HTTP status checks | Error bodies parsed as protobuf |
| C9 | High | `protobufClient/profiles.go` | No client/request timeouts | Stuck requests pin goroutines forever |
| C10 | Medium | `protobufClient/profiles.go` | No startup stagger | 10k simultaneous connection attempts |
| C11 | Medium | `protobufClient/profiles.go` | Player goroutine exits on error | Active user count only decreases |
| M1 | **Critical** | `matchmaking_service/matchmaking.go` | Global mutex + Redis `SCAN` per player every 10 ms | O(n²) lock convoy, queue timeouts |
| M2 | **Critical** | `matchmaking_service/matchmaking.go` | Background goroutine writes other players' `ResponseWriter` | Data race, corrupted/dropped responses |
| M3 | Low | `matchmaking_service/matchmaking.go` | `defer r.Context().Done()` | No-op; cancellation not observed |
| M4 | High | `matchmaking_service/matchmaking.go` | `context.Background()` for DB/Redis | No timeouts; slow dependency pins goroutines |
| M5 | High | `matchmaking_service/main.go` | Default Redis pool (`10 × GOMAXPROCS`) | Pool exhaustion at 10k players |
| M6 | High | `matchmaking_service/matchmaking.go` | No atomic claim of matched players | Partial/overlapping matches |
| M7 | Medium | `matchmaking_service/matchmaking.go` | Per-call `rand.NewSource(time.Now())` | Duplicate match IDs |
| M8 | High | `matchmaking_service/matchmaking.go` | Nil deref in validation | Panic on malformed body |
| M9 | Low | `matchmaking_service/matchmaking.go` | Rank check `>44 && <0` always false | Invalid ranks accepted |
| M10 | Medium | `matchmaking_service/matchmaking.go` | New client per spawn, body not drained | Connection churn, no reuse |
| M11 | Medium | `matchmaking_service/matchmaking.go` | Unbounded spawn goroutines | Thundering herd on game server |
| M12 | High | `matchmaking_service/matchmaking.go` | Provision failure drops players | Users lost on transient errors |
| M13 | High | `matchmaking_service/matchmaking.go` | Disconnected players never pruned | Ghost players fill matches |
| M14 | Low | `matchmaking_service/matchmaking.go` | 10 sequential Redis `DEL`s per match | Extra latency under load |
| M15 | High | `matchmaking_service/main.go` | No HTTP server timeouts | Slow/half-open clients pin connections |
| M16 | High | `matchmaking_service/metrics.go` | No panic recovery | Panic silently drops the connection |
| M17 | Medium | `matchmaking_service/matchmaking.go` | Confused cancellation contexts | Queue entries outlive their requests |
| G1 | **Critical** | `game_server_service/gameServer.go` | Global `databaseTransactionMutex` | All DB writes serialized |
| G2 | **Critical** | `game_server_service/gameServer.go` | Infinite polling loops, no deadline | Goroutine/memory leak per stuck match |
| G3 | **Critical** | `game_server_service/randomizedMatch.go` | Unlocked Load→append→Store | Lost participant appends; matches never fill |
| G4 | **Critical** | `game_server_service/randomizedMatch.go` | Unlocked Load→mutate→Store match data | Concurrent writes of conflicting results |
| G5 | **Critical** | `game_server_service/gameServer.go` | `wg.Add` outside the counting mutex | Add-after-Wait race, premature cleanup |
| G6 | High | `game_server_service/gameServer.go` | Cleanup only by the "10th" player | Leaked maps when that request fails |
| G7 | High | `game_server_service/gameServer.go` | Duplicate PUUID connections accepted | Inflated counts, 11th participant |
| G8 | High | `game_server_service/gameServer.go` | Missing match silently ignored | Client waits forever |
| G9 | High | `game_server_service/gameServer.go` | Unchecked type assertions | Panic on unexpected value |
| G10 | High | `game_server_service/main.go` | `UnpackCreationRequest` error ignored | Nil deref panic |
| G11 | **Critical** | all three `initDB` | `pool_max_conns=10000` vs `max_connections=100` | `too many clients` |
| G12 | High | `game_server_service/gameServer.go` | `context.Background()` for queries | No query timeouts |
| G13 | High | `dbScripts/init.sql` | No index on `matchHistory(puuid)` | Sequential scan as table grows |
| G14 | High | `game_server_service/metrics.go` | No panic recovery | Silent connection drop |
| G15 | Medium | `game_server_service/main.go` | Janitor commented out | Abandoned matches never reaped |
| G16 | Medium | `game_server_service/randomizedMatch.go` | Slice bounds checked after slicing | Panic on short match ID |
| G17 | Medium | `game_server_service/randomizedMatch.go` | Champion/item tables rebuilt per call | Allocation storm, GC pressure |
| G18 | Low | `game_server_service/gameServer.go` | 250 ms poll granularity | Slow participant detection |
| G19 | Medium | `game_server_service/main.go` | No HTTP server timeouts | Slow clients pin connections |
| G20 | Medium | `game_server_service/gameServer.go` | Response written before background work | No tracking of in-flight writes |
| P1 | High | `platform_service/platform.go` | `context.Background()` for queries | No query timeouts |
| P2 | High | `platform_service/platform.go` | `pool_max_conns=10000` | `too many clients` |
| P3 | Medium | `platform_service/platform.go` | `rows.Err()` not checked | Silent partial results |
| P4 | **High** | `platform_service/platform.go` | Empty profile returned `http.Error` | Client unmarshal failure → `log.Fatalf` |
| P5 | High | `platform_service/platform.go` | `UnpackRequest` errors ignored | Nil deref |
| P6 | High | `platform_service/platform.go` | Query error logged but no response | Client hangs until timeout |
| P7 | Low | `platform_service/platform.go` | Timestamps computed before `Scan` | Wrong values on scan failure |
| P8 | High | `platform_service/main.go` | No HTTP server timeouts | Slow clients pin connections |
| P9 | High | `platform_service/metrics.go` | No panic recovery | Silent connection drop |
| I1 | **Critical** | `dbScripts/init.sql` | `max_connections=100` | Too few for three pools |
| I2 | High | `docker-compose.yaml` | No `nofile` ulimits | `EMFILE` at 10k sockets |
| I3 | High | `docker-compose.yaml` | `cpuset: "0,1,2,3"` on everything | CPU starvation for all services + DB |
| I4 | Medium | `docker-compose.yaml` | Default `shared_buffers`, no `shm_size` | Poor Postgres throughput |
| P10 | **Critical** | `platform_service/platform.go` | `/matchHistory` had no `ORDER BY`/`LIMIT` | Response grows with run length; 10k users time out after ~25 min |
| I5 | High | `dbScripts/init.sql` | No `(puuid, gameEndTimestamp DESC)` index | Every history read scans the player's full history |

---

## 6. Client bugs (`protobufClient`)

The load generator is the part that *reported* the errors, but it was also a major cause of
them.

### C1 — `defer Body.Close()` inside an infinite loop

**Before**

```go
for {
    streamResponse, err := client.Do(resp)
    ...
    defer streamResponse.Body.Close()   // never runs: the loop never exits
    ...
}
```

A `defer` runs when the **function** returns. The player loop never returns, so every
iteration leaked one file descriptor. With 10k players and several requests per cycle, the
process hit `EMFILE` within minutes — which is exactly the
`connect: resource temporarily unavailable` error.

**After**

```go
func doProto(ctx context.Context, client *http.Client, url string, msg proto.Message) ([]byte, error) {
    ...
    resp, err := client.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()             // runs on every call, not every process
    body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
    ...
}
```

The helper returns per request, so the close actually happens.

---

### C2 — A new `http.Client` per request

**Before**

```go
client := &http.Client{}
streamResponse, err := client.Do(resp)
...
matchClient := &http.Client{}
...
matchHistoryClient := &http.Client{}
...
summonerProfileClient := &http.Client{}
```

Every `http.Client` with a nil `Transport` gets its own `http.Transport`, hence its own
connection pool. Nothing was reused; each call opened a new TCP connection and left the old
one in `TIME_WAIT`. This exhausts ephemeral ports and multiplies goroutines.

**After**

```go
var sharedTransport = &http.Transport{
    Proxy:               http.ProxyFromEnvironment,
    DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
    ForceAttemptHTTP2:   false,
    MaxIdleConns:        0,      // no global cap
    MaxIdleConnsPerHost: 20000,  // default of 2 defeats pooling at this scale
    MaxConnsPerHost:     0,      // unlimited active
    IdleConnTimeout:     90 * time.Second,
}

var (
    queueClient = &http.Client{Transport: sharedTransport}                    // long-lived
    apiClient   = &http.Client{Transport: sharedTransport, Timeout: apiTimeout} // short calls
)
```

---

### C3 — Chunked read + unmarshal

**Before**

```go
buf := make([]byte, maxBufferSize)
for {
    n, err := streamResponse.Body.Read(buf)
    ...
    data := buf[:n]
    for len(data) > 0 {
        err := proto.Unmarshal(data, &response)  // decodes a partial message
        ...
        data = data[len(data):]                  // always empties the slice
    }
    if err == io.EOF { break }
}
```

`Body.Read` returns whatever bytes are available, not a complete protobuf message. A message
split across two TCP segments fails to decode (`cannot parse invalid wire-format data`), and
the goroutine exits. The `data = data[len(data):]` line also makes the inner loop a no-op.

**After**

```go
body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
if err != nil { return nil, fmt.Errorf("read response: %w", err) }
if resp.StatusCode != http.StatusOK { ... }
if len(body) == 0 { ... }
return body, nil
```

The caller unmarshals the complete body exactly once.

---

### C4 — `log.Fatalf` inside player goroutines

**Before**

```go
body, err := io.ReadAll(matchHistoryResponse.Body)
if err != nil {
    log.Fatalf("Failed to read response body: %v\n", err)   // os.Exit(1)
}
err = proto.Unmarshal(body, &protoResponse)
if err != nil {
    log.Fatalf("Failed to unmarshal match history response: %v\n", err)  // os.Exit(1)
}
```

A single player's bad response killed the **entire load generator**, dropping all 10k
connections at once. This is why the test sometimes appeared to "just stop".

**After**

```go
if err := proto.Unmarshal(body, &history); err != nil {
    return fmt.Errorf("unmarshal matchHistory response (%d bytes): %w", len(body), err)
}
```

Errors return to `runPlayerCycle`, which backs off and retries the player.

---

### C5 — Shared `*rand.Rand`

**Before**

```go
rng := rand.New(src)          // one RNG for the whole test
for i := 0; i < len(partyRequests); i++ {
    go func(pr *olympusProto.Players) {
        ... RandomIntInRange(rng, ...)   // concurrent use
    }(partyRequests[i])
}
```

`math/rand.Rand` is **not** safe for concurrent use. Concurrent calls corrupt its internal
state (and are a data race under `-race`).

**After**

```go
go func(index int) {
    playerRng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(index)))
    ...
}(i)
```

---

### C6 — `RandomIntInRange` called `log.Fatalf`

**Before**

```go
func RandomIntInRange(rng *rand.Rand, min, max int) int {
    if min > max {
        log.Fatalf("min cannot be greater than max")   // exits the process
    }
    return rng.Intn(max-min+1) + min
}
```

A helper function running in a goroutine must never terminate the process.

**After**

```go
if min > max {
    log.Printf("RandomIntInRange: min (%d) cannot be greater than max (%d)", min, max)
    min, max = max, min
}
```

---

### C7 — No backoff on retry

**Before**

```go
streamResponse, err := client.Do(resp)
if err != nil {
    continue        // immediately retries, in a tight loop
}
```

When the server was struggling, 10k goroutines spun at full CPU retrying, making the overload
worse.

**After**

```go
if err := runPlayerCycle(playerRng, player); err != nil {
    logFailure("player %s: %v", shortPUUID(player.PlayerPuuid), err)
    time.Sleep(time.Duration(RandomIntInRange(playerRng, 1, 3)) * time.Second)
    continue
}
```

---

### C8 — No HTTP status checks

**Before** — any response body was parsed as protobuf, including `500` error text.

**After**

```go
if resp.StatusCode != http.StatusOK {
    return nil, fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, truncate(string(body), 200))
}
```

---

### C9 — No timeouts

**Before** — `&http.Client{}` has **no timeout**. A half-open server connection pins the
goroutine forever, and the player never retries.

**After**

```go
queueTimeout = 10 * time.Minute   // /queueUp legitimately waits
apiTimeout   = 15 * time.Second   // short post-match calls
// plus net.Dialer{Timeout: 10s}, IdleConnTimeout: 90s
```

`/queueUp` is bounded by a context, not by `Client.Timeout`, because the request is
intentionally long-lived.

---

### C10 — No startup stagger

**Before** — all 10k goroutines fired at once.

**After**

```go
time.Sleep(time.Duration(playerRng.Intn(2000)) * time.Millisecond)
```

---

### C11 — A player that errored never came back

**Before** — on any error the goroutine did `return`. The outer `counter` only decremented,
so the number of active users shrank over time.

**After** — the per-player loop is infinite; a failure backs off and retries the same player.

---

## 7. Matchmaking bugs (`matchmaking_service`)

### M1 — Global mutex + full Redis `SCAN` on every player poll

**Before**

```go
func MatchmakingSelection(w, unpackedRequest, rdb, ctx, partyResourcesMap, mu, myRank) bool {
    var matchedParties []matchedParty
    mu.Lock()                       // global lock
    defer mu.Unlock()

    var newCursor uint64
    for len(matchedParties) < 9 {
        keys, nextCursor, err := rdb.Scan(ctx, newCursor, "*", 100).Result()
        ...
    }
    ...
}
```

Called from `MatchFinder`:

```go
ticker := time.NewTicker(10 * time.Millisecond)
for {
    select {
    case <-ticker.C:
        if MatchmakingSelection(...) { return }
    }
}
```

At 10k players:

- 10k players × 100 polls/sec = **1,000,000 calls/sec**
- each call takes a **process-wide mutex**
- each call can **scan the entire Redis keyspace**

The handlers are effectively serialized, and each one does network I/O while holding the
lock. `/queueUp` latency grows without bound → client timeouts and resets.

**After**

```go
const matchmakerInterval = 250 * time.Millisecond

type Matchmaker struct {
    mu      sync.Mutex
    waiting map[string]*PartyResources
    rdb     *redis.Client
    ctx     context.Context
    sem     chan struct{}
}

func (m *Matchmaker) run() {
    ticker := time.NewTicker(matchmakerInterval)
    for {
        select {
        case <-m.ctx.Done(): return
        case <-ticker.C:     m.formMatches()
        }
    }
}
```

One worker, four passes per second. `formMatches` snapshots the queue, sorts by rank, groups
in O(n log n), **releases the lock**, then provisions outside it:

```go
m.mu.Lock()
// snapshot + drop dead requests + sort
...
// claim groups atomically
m.mu.Unlock()

for _, group := range claimed {
    m.sem <- struct{}{}
    go func() { defer func() { <-m.sem }(); m.startMatch(group) }()
}
```

---

### M2 — Writing another player's `ResponseWriter` from a background goroutine

**Before**

```go
type PartyResources struct {
    CancelFunc context.CancelFunc
    Writer     http.ResponseWriter   // stored, then written from elsewhere
}
...
if partyCtx, ok := partyResourcesMap.Load(partyKey); ok {
    if ctx, ok := partyCtx.(PartyResources); ok {
        ctx.Writer.Write([]byte(data))   // not the owning handler's goroutine
        ctx.CancelFunc()
    }
}
```

`net/http` requires that a `ResponseWriter` is only used by the goroutine that owns the
request. Writing to it from the matchmaking worker is a data race and can corrupt the
response or drop the connection.

**After**

```go
type PartyResources struct {
    MatchChan chan []byte      // only the owning handler writes to w
    Player    *matchmakingProto.Players
    Rank      int
    QueuedAt  time.Time
    Ctx       context.Context
    cancelled atomic.Bool
}
...
select {
case party.MatchChan <- data:
default:   // handler already gone
}
```

---

### M3 — `defer r.Context().Done()`

`Done()` returns a channel; deferring it does nothing. It was intended to register cleanup
but never did. Replaced with an explicit deferred teardown block.

---

### M4 — No timeouts on DB / Redis

**Before** — `context.Background()` for the rank query, and the Redis client's default
timeouts.

**After**

```go
dbCtx, dbCancel := context.WithTimeout(r.Context(), dbTimeout)       // 5s
defer dbCancel()

redisCtx, redisCancel := context.WithTimeout(r.Context(), redisTimeout) // 2s
AddPartyToRedis(w, unpackedRequest, myRank, rdb, redisCtx)
redisCancel()
```

---

### M5 — Redis pool too small

go-redis defaults to `PoolSize = 10 × GOMAXPROCS`. With thousands of concurrent players the
pool is exhausted and callers block.

**After**

```go
rdb := redis.NewClient(&redis.Options{
    Addr:         redisAddr,
    PoolSize:     200,
    MinIdleConns: 20,
    PoolTimeout:  5 * time.Second,
    DialTimeout:  5 * time.Second,
    ReadTimeout:  3 * time.Second,
    WriteTimeout: 3 * time.Second,
})
```

---

### M6 — No atomic claim

The old code scanned Redis, appended matches, then deleted keys one at a time. Two players
could select the same teammates, or a partial group could be deleted, producing matches with
fewer than 10 players.

**After** — groups are claimed by deleting them from `m.waiting` **while still holding the
lock**, and re-verified before claiming:

```go
stillWaiting := true
for _, party := range group {
    if _, ok := m.waiting[party.Player.PartyId]; !ok { stillWaiting = false; break }
}
if !stillWaiting { continue }
for _, party := range group { delete(m.waiting, party.Player.PartyId) }
```

---

### M7 — Duplicate match IDs

**Before**

```go
src := rand.NewSource(time.Now().UnixNano())
r := rand.New(src)
id[i] = charset[r.Intn(len(charset))]
```

Two matches formed in the same nanosecond could receive the same ID, colliding in the game
server's maps.

**After**

```go
id[i] = charset[rand.Intn(len(charset))]   // package-level rand: safe + auto-seeded
```

---

### M8 — Nil dereference on malformed input

`UnpackRequest` returns `nil` on read/unmarshal failure, and validation immediately
dereferenced it:

```go
case unpackedRequest.PartyId[:6] != "PARTY_":   // panic if nil
```

**After**

```go
if unpackedRequest == nil { return false }
```

---

### M9 — Impossible rank check

```go
case unpackedRequest.PlayerRank > 44 && unpackedRequest.PlayerRank < 0:
```

No value satisfies both. Invalid ranks were accepted.

**After**

```go
case unpackedRequest.PlayerRank < 0 || unpackedRequest.PlayerRank > 44:
```

---

### M10 — New HTTP client per spawn, body not drained

**Before**

```go
client := &http.Client{Timeout: 5 * time.Second}   // per call
resp, err := client.Do(req)
defer resp.Body.Close()                             // not drained → no reuse
```

**After**

```go
var provisionClient = &http.Client{Timeout: 5 * time.Second}   // shared

resp, err := provisionClient.Do(req)
defer resp.Body.Close()
_, _ = io.Copy(io.Discard, resp.Body)   // drain so the connection is reused
```

---

### M11 — Unbounded spawning

`startMatch` was called directly for every group. A backlog could spawn hundreds of game
servers at once.

**After** — a 16-slot semaphore:

```go
const provisionConcurrency = 16
sem: make(chan struct{}, provisionConcurrency)
```

---

### M12 — Provisioning failure dropped players

**Before**

```go
if ProvisionGameServer(data) {
    ...
} else {
    w.Write([]byte("Failed to provision game server"))
    // cancel everyone
}
```

A transient game-server error removed 10 players from the test.

**After**

```go
if !ProvisionGameServer(data) {
    log.Printf("Failed to provision game server for match %s; re-queueing %d players", ...)
    m.requeue(group)
    return
}
```

---

### M13 — Dead requests never pruned

Disconnected players stayed in Redis and could be selected into matches, producing groups
that could never fill.

**After**

```go
if party.dead() {
    delete(m.waiting, party.Player.PartyId)
    continue
}
```

with

```go
func (p *PartyResources) dead() bool {
    return p.cancelled.Load() || (p.Ctx != nil && p.Ctx.Err() != nil)
}
```

---

### M14 — 10 sequential Redis deletes

The old code deleted each matched key with a separate round trip after the match. Now each
handler removes its own key in its deferred teardown, concurrently.

---

### M15 — No HTTP server timeouts

**Before** — `log.Fatal(http.ListenAndServe(port, nil))` uses no timeouts at all.

**After**

```go
srv := &http.Server{
    Addr:              port,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      0,                 // /queueUp is long-lived by design
    IdleTimeout:       120 * time.Second,
    MaxHeaderBytes:    1 << 20,
}
```

`WriteTimeout` **must** stay 0 here: a player can legitimately wait minutes for a match.
The request context still cancels the handler the instant the client disconnects.

---

### M16 — No panic recovery

A panic inside a handler let `net/http` drop the connection with no log.

**After**

```go
defer func() {
    if rec := recover(); rec != nil {
        log.Printf("panic in %s: %v\n%s", endpoint, rec, debug.Stack())
        http.Error(w, "internal server error", http.StatusInternalServerError)
    }
    ...
}()
```

---

### M17 — Confused cancellation

The old `MatchFinder` selected on a `matchmakingContext` created with
`context.WithCancel(context.Background())` and cancelled by *other* players' handlers. A
player's own disconnect only affected `requester.Context()`. Queue entries could outlive the
request.

**After** — the player's request context is the single source of truth (`party.Ctx`), plus an
explicit `cancelled` flag set in the deferred teardown.

---

## 8. Game server bugs (`game_server_service`)

### G1 — Global database mutex

**Before**

```go
var databaseTransactionMutex sync.Mutex
...
databaseTransactionMutex.Lock()
defer databaseTransactionMutex.Unlock()
err = UpdateProfile(...)
...
_, err = dbPool.Exec(context.Background(), `INSERT INTO "matchHistory" ...`)
```

Every player in every match serialized behind **one process-wide mutex** for two database
round trips. At 10k players that is ~100k writes queued behind a single lock. Handlers pile
up, connections time out, and the client sees resets.

**After** — the mutex is gone. `pgxpool` is safe for concurrent use, and each player writes
its own independent rows. The pool (50 connections) provides the real backpressure.

---

### G2 — Infinite loops with no deadline

**Before**

```go
for {
    if len(match.ParticipantsPUUID) != 10 {
        time.Sleep(250 * time.Millisecond)
        continue
    }
    ...
    for {
        time.Sleep(250 * time.Millisecond)
        participantValue, ok := matchParticipantsMap.Load(match.MatchID)
        ...
        if len(randomParticipants) != 10 {
            continue        // forever
        }
        ...
    }
}
```

If a single participant never connected, the other nine goroutines spun **for the lifetime of
the process**. Each one holds the request, its buffers, and its stack.

**After**

```go
const (
    matchConnectTimeout = 2 * time.Minute
    participantPoll     = 25 * time.Millisecond
)

deadline := time.Now().Add(matchConnectTimeout)
for {
    ...
    if time.Now().After(deadline) {
        return fmt.Errorf("timed out waiting for %d participants in match %s", matchSize, match.MatchID)
    }
    time.Sleep(participantPoll)
}
```

---

### G3 — Lost participant appends

**Before**

```go
func addParticipant(matchID string, matchParticipantsMap *sync.Map, participant *gameServerProto.Participant) {
    value, _ := matchParticipantsMap.Load(matchID)
    var participants []*gameServerProto.Participant
    if value != nil {
        participants = value.([]*gameServerProto.Participant)
    }
    participants = append(participants, participant)
    matchParticipantsMap.Store(matchID, participants)
}
```

`sync.Map` is safe for individual `Load`/`Store`, but **not** for the Load→append→Store
sequence. Two players can read the same slice; the second `Store` overwrites the first
append. The match then **never reaches 10 participants** — which feeds G2's infinite loop.

**After** — a per-match lock:

```go
var participantLocks sync.Map

func participantLock(matchID string) *sync.Mutex {
    lock, _ := participantLocks.LoadOrStore(matchID, &sync.Mutex{})
    return lock.(*sync.Mutex)
}

func addParticipant(...) {
    lock := participantLock(matchID)
    lock.Lock()
    defer lock.Unlock()
    ...
}
```

The lock is released when the match is cleaned up (`releaseParticipantLock`).

---

### G4 — Match data race

`generateRandomMatchData` had the same Load→mutate→Store pattern. Two participants could
both observe an empty entry and store different `MatchResult`s.

**After** — the same per-match lock wraps the whole read-modify-write.

---

### G5 — WaitGroup `Add` outside the counting mutex

**Before**

```go
tracker.mu.Lock()
tracker.count++
isTenthPlayer := (tracker.count == 10)
tracker.mu.Unlock()

tracker.wg.Add(1)          // outside the lock!
...
if isTenthPlayer {
    tracker.wg.Wait()      // can run before the 9th Add(1)
    // cleanup
}
```

The 10th player could reach `wg.Wait()` before the 9th player had called `wg.Add(1)`. That
is the classic `sync: WaitGroup is reused before previous Wait has returned` panic — or, more
commonly here, **premature cleanup** that deletes maps while handlers are still using them,
dropping connections.

**After**

```go
func (t *matchTracker) arrive(puuid string) (isLast bool, duplicate bool) {
    t.mu.Lock()
    defer t.mu.Unlock()

    if _, seen := t.seen[puuid]; seen { return false, true }
    if t.arrived >= t.expected { return false, true }

    t.seen[puuid] = struct{}{}
    t.wg.Add(1)                 // under the same lock as the counter
    t.arrived++
    if t.arrived == t.expected {
        t.doneOnce.Do(func() { close(t.done) })
        return true, false
    }
    return false, false
}
```

Because `Add` and the count increment happen under the same mutex, the last arrival is
guaranteed to observe every earlier registration.

---

### G6 — Cleanup depended on the "10th" player

The handler that happened to be 10th did the map cleanup. If that specific request failed or
panicked, the maps leaked forever.

**After** — three safety layers:

1. The completing handler waits and calls `cleanOnce.Do(cleanup)`.
2. A `time.AfterFunc(matchConnectTimeout, ...)` watchdog calls the same `cleanOnce`.
3. A janitor (`matchHeartBeat`, every 5 minutes) deletes anything older than an hour.

```go
cleanup := func() {
    activeMatches.Delete(matchID)
    matchDataMap.Delete(matchID)
    matchParticipantsMap.Delete(matchID)
    matchCreationDates.Delete(matchID)
    waitGroupMap.Delete(matchID)
    releaseParticipantLock(matchID)
}
```

---

### G7 — Duplicate connections

A client retry could connect the same PUUID twice, producing 11 participants, corrupting the
WaitGroup accounting, and inflating `activeConnections`.

**After**

```go
if _, seen := t.seen[puuid]; seen { return false, true }
...
if duplicate {
    http.Error(w, "Duplicate connection", http.StatusConflict)
    return
}
```

---

### G8 — Missing match silently ignored

**Before**

```go
validateSyncStore, ok := activeMatches.Load(unpackedRequest.MatchID)
if ok {
    ...
}
// no else: handler returns with no response
```

The client waited for a body that never came, until its own timeout.

**After**

```go
if !ok {
    log.Printf("No active match for matchID: %s", matchID)
    http.Error(w, "Match not found", http.StatusNotFound)
    return
}
```

---

### G9 — Unchecked type assertions

`participantValue.([]*gameServerProto.Participant)` panics if the stored value is not that
type. Now every assertion is checked (`, ok`).

---

### G10 — Ignored unpack error

**Before**

```go
unpackedRequest, _ := UnpackCreationRequest(w, r)
matchesSpawned.Inc()
activeMatches.Store(unpackedRequest.MatchID, unpackedRequest)   // nil deref
```

**After**

```go
unpackedRequest, err := UnpackCreationRequest(w, r)
if err != nil { return }   // the helper already wrote the error response
```

---

### G11 — 10,000 connections per service

**Before** — in **all three** `initDB` functions:

```go
dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=10000"
```

Three services × 10,000 = **30,000** potential connections, while Postgres allowed **100**.

**After**

```go
dsn := "postgres://sawa:sawa@postgres:5432/olympus?sslmode=disable&pool_max_conns=50&pool_min_conns=5&pool_max_conn_idle_time=1m&pool_max_conn_lifetime=30m"
```

50 × 3 = 150 connections against `max_connections=300`, leaving headroom for tooling.

---

### G12 — No query timeouts

**Before** — `dbPool.QueryRow(context.Background(), ...)`, `dbPool.Exec(context.Background(), ...)`.

**After**

```go
const dbOpTimeout = 5 * time.Second
ctx, cancel := context.WithTimeout(context.Background(), dbOpTimeout)
defer cancel()
```

---

### G13 — Missing index

`/matchHistory` filters on `puuid`, but the unique constraint is ordered `(matchID, puuid)`,
so it cannot serve a `puuid` lookup. As the table grew past 300k rows this became a
sequential scan.

**After**

```sql
CREATE INDEX IF NOT EXISTS idx_match_history_puuid ON "matchHistory" ("puuid");
```

---

### G14 — No panic recovery

Same fix as M16.

---

### G15 — Janitor was commented out

`matchHeartBeat` existed only as commented-out code, so any match that slipped past normal
cleanup lived forever. It is now active and clears **all five** maps plus the participant
lock.

---

### G16 — Slice bounds checked in the wrong order

**Before**

```go
} else if participants.MatchID[:6] != "MATCH_" || len(participants.MatchID) < 7 {
```

`MatchID[:6]` is evaluated first and panics for IDs shorter than 6 characters.

**After**

```go
} else if len(participants.MatchID) < 7 || participants.MatchID[:6] != "MATCH_" {
```

---

### G17 — Lookup tables rebuilt on every call

`getRandomChamp` built a ~170-entry map on **every** call; `getRandomItems` allocated a
hundreds-of-elements slice on every call. With 10 participants per match and thousands of
matches, this is an allocation storm.

**After** — built once with `sync.Once`:

```go
var (
    championOnce  sync.Once
    champions     map[string]string
    championNames []string
)
```

---

### G18 — Poll granularity

The participant poll dropped from 250 ms to 25 ms, so a match starts promptly once the 10th
player arrives, without busy-waiting.

---

### G19 — No HTTP server timeouts

**After**

```go
srv := &http.Server{
    Addr:              port,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      30 * time.Second,   // /connectToMatch replies immediately
    IdleTimeout:       120 * time.Second,
    MaxHeaderBytes:    1 << 20,
}
```

---

### G20 — Response written before background work

The handler acknowledges immediately, then records the result in a background goroutine. That
design is kept (it keeps `/connectToMatch` fast), but the goroutine now has:

- a `defer tracker.wg.Done()`
- a panic recovery
- bounded DB calls
- a `cleanOnce` cleanup path

so in-flight writes are tracked and cannot leak.

---

## 9. Platform bugs (`platform_service`)

### P1 — No query timeouts

`context.Background()` for every query. Now `context.WithTimeout(r.Context(), dbOpTimeout)`.

### P2 — Pool of 10,000

Same fix as G11: `pool_max_conns=50`.

### P3 — `rows.Err()` not checked

After `rows.Next()` returns false, the old code assumed "no data". A mid-stream error looked
like an empty result. Now:

```go
if !rows.Next() {
    if err := rows.Err(); err != nil {
        http.Error(w, "Database query failed", http.StatusInternalServerError)
        return
    }
    ...
}
```

### P4 — Empty profile returned an HTTP error

**Before**

```go
if !rows.Next() {
    log.Printf("No summmoner data for PUUID: %s\n", unpackedRequest.Puuid)
    //http.Error(w, "No data found", http.StatusNotFound)
    return
}
```

Returning `http.Error` (plain text) for a protobuf endpoint made the client fail to
unmarshal — and the old client responded with `log.Fatalf`, killing the whole test.

**After** — a valid empty protobuf message:

```go
response := &platformProto.UserProfile{Puuid: unpackedRequest.Puuid}
data, _ := proto.Marshal(response)
w.Header().Set("Content-Type", "application/x-protobuf")
w.Write(data)
```

### P5 — Ignored unpack errors

`UnpackRequest` errors now return `400` instead of dereferencing a nil message.

### P6 — Query error logged but no response

`GetMatchHistory` logged the error and then continued, leaving the client waiting for a body.
It now returns `500` immediately.

### P7 — Timestamps computed before `Scan`

Minor correctness: `gameStartTime`/`gameEndTime` were formatted before the row was scanned.
They are now built after a successful `Scan`.

### P8 — No HTTP server timeouts

Same `http.Server` configuration as the other services (`WriteTimeout: 30s` is fine here).

### P9 — No panic recovery

Same middleware recovery as M16/G14.

---

## 10. Infrastructure bugs

### I1 — `max_connections=100`

**Before**

```sql
ALTER SYSTEM SET max_connections = 100;
```

**After**

```sql
-- Keep this >= the sum of all pgx pool sizes
-- (matchmaking 50 + game server 50 + platform 50) plus headroom.
ALTER SYSTEM SET max_connections = 300;
```

`ALTER SYSTEM` only takes effect on restart, so the compose file also passes it on the
command line:

```yaml
command: ["postgres", "-c", "max_connections=300", "-c", "shared_buffers=256MB"]
```

### I2 — No `nofile` ulimits

Containers inherited the default (often 1024). At 10k sockets a service hits `EMFILE`
immediately.

**After** — every service, Redis, and Postgres gets:

```yaml
ulimits:
  nofile:
    soft: 65536
    hard: 65536
```

### I3 — Everything pinned to 4 CPUs

**Before**

```yaml
cpuset: "0,1,2,3"
```

This was applied to the game server, platform server, Prometheus, blackbox, Grafana, **and
Postgres**. The entire stack competed for four cores while simulating 10k users.

**After** — removed.

### I4 — Postgres defaults

`shared_buffers` stayed at the default and `shm_size` was unset (Docker's 64 MB default),
which limits Postgres performance and parallel query workers.

**After**

```yaml
command: ["postgres", "-c", "max_connections=300", "-c", "shared_buffers=256MB"]
shm_size: "256mb"
```

---

## 11. Why the fixes work together

The fixes are not independent — they break the feedback loops:

| Loop | Broken by |
|---|---|
| FD leak → `EMFILE` → more retries → more FDs | C1, C2, C7, C9 |
| Partial parse → player dies → fewer users → no recovery | C3, C11 |
| Global mutex + `SCAN` → latency → timeouts → resets | M1, M2, M15 |
| 30k pool conns vs 100 allowed → every DB op fails | G11, P2, I1 |
| Global DB mutex → write queue → timeouts | G1, G12, G13 |
| Lost appends → match never fills → infinite loops → OOM | G3, G4, G2, G5, G6 |
| Panic → silent connection drop → client retry → more load | M16, G14, P9, C8 |

After the fixes, the steady state at 10k users is:

- matchmaking holds only an **in-memory map** and wakes **4×/second**
- game-server DB writes run **concurrently** through a bounded 50-connection pool
- every wait has a **deadline**
- every map entry has a **cleanup path**
- the client reuses **one transport** and closes **every** response body

---

## 12. Validation

### Build and static checks

```bash
cd olympus/matchmaking_service  && go build ./... && go vet ./...
cd olympus/game_server_service && go build ./... && go vet ./...
cd olympus/platform_service    && go build ./... && go vet ./...
cd protobufClient               && go build ./... && go vet ./...
gofmt -l .
git diff --check
```

All clean.

### Race tests

Temporary `-race` tests (removed afterwards) exercised:

- `addParticipant` with concurrent appends
- `generateRandomMatchData` with concurrent writers
- `matchTracker.arrive` with concurrent arrivals

All passed.

### Load tests

Environment: rootless Redis 7.4.2 + PostgreSQL 16.9 built under `/tmp/opencode`,
`max_connections=300`, three services at 50 pool connections each.

| Run | Duration | Matches | History rows | Errors |
|---|---|---|---|---|
| 500 users | 20 s | 1,850 | 1,851 | 0 |
| 10,000 users | 90 s | 16,160 | 159,580 | 0 |
| 10,000 users | 180 s | 31,601 | 316,010 | 0 |

Resource behaviour during the 180 s run:

| Metric | 60 s | 120 s | 180 s |
|---|---|---|---|
| Client FDs | 3,353 | 1,923 | 1,836 |
| Client RSS | 341 MB | 327 MB | 309 MB |
| Matchmaking RSS | 96 MB | 86 MB | 82 MB |
| Game server RSS | 82 MB | 77 MB | 75 MB |
| Platform RSS | 102 MB | 88 MB | 111 MB |
| Postgres connections | 151 | 151 | 151 |

- Client FDs **converge downward** instead of growing — the leak is gone.
- Postgres sits at **151/300** connections, exactly the expected 3 × 50 + tooling.
- Zero errors in the service logs during the run.
- Redis returns to **0 keys** after cleanup.
- The only log lines at shutdown are three benign `context canceled` messages from the
  platform service at the exact instant the client was killed.

---

## 13. How to reproduce

### Full Docker stack

```bash
cd /home/sawa/devenv/olympus
docker compose up -d --build \
  matchmaking_redis postgres matchmaking_server game_server platform_server

for p in 8080 8081 8082; do
  curl -s -o /dev/null -w "$p -> %{http_code}\n" localhost:$p/health
done
curl -s -o /dev/null -w "db -> %{http_code}\n" localhost:8082/databaseHealth
```

### Load generator

```bash
cd /home/sawa/devenv/protobufClient
go build -o /tmp/opencode/client .

# sanity
timeout 20s /tmp/opencode/client -users 500

# full run
timeout 180s /tmp/opencode/client -users 10000
```

### What to watch

```bash
# matches and active players
curl -s localhost:8081/metrics | grep -E 'matches_spawned|game_server_queueup_active_players'
curl -s localhost:8080/metrics | grep matchmaking_queueup_active_players

# Postgres connections
docker compose exec -T postgres psql -U sawa -d olympus \
  -c "select count(*) from pg_stat_activity where datname='olympus';"

# client FDs
ls /proc/$(pgrep -x client)/fd | wc -l
```

### Reset between runs

```bash
docker compose exec -T postgres psql -U sawa -d olympus \
  -c 'TRUNCATE "matchHistory", "summonerRankedInfo";'
docker compose exec -T matchmaking_redis redis-cli flushall
```

### Dashboards

`grafana_dashboards/` contains importable JSON for Load Test Overview, Service Health, and
Runtime/Resources. See `grafana_dashboards/README.md`.

---

## 14. What was intentionally not changed

- **`protobufClient/test.go`** — dead code (`func test()`), left untouched.
- **`NewPlayerConnectionBackup`** in `gameServer.go` — unused backup handler, left untouched.
- **The simulated game duration** (`time.Sleep(250 * time.Millisecond)` in
  `ConnectPlayerToMatch`) — intentional, not a bug.
- **The `/queueUp` streaming design** — kept; `WriteTimeout: 0` is required for it.
- **No commits or pushes** were made to either repository.

---

## 15. Remaining limitations

These are known gaps, not regressions:

1. **No backoff before requeue** when game-server provisioning fails. If the game server is
   down, the matchmaker will retry the same group every 250 ms. A small exponential backoff
   would be kinder.
2. **`context canceled` log noise** in the platform service when a client disconnects
   mid-query. Harmless, but noisy.
3. **No Postgres metrics** — there is no `postgres_exporter`, so pool saturation and row
   throughput are invisible to Prometheus.
4. **No status-code labels** on the HTTP metrics, so a true error-rate panel is not possible
   without changing the metric definitions.
5. **`matches_spawned` is a gauge** that is incremented. It resets on restart; treat it as
   "since start", not a monotonic counter.
6. **Single matchmaker worker** — intentional (it is fast enough), but it is a single point
   of serialization if matchmaking logic grows expensive.

---

## 16. File change index

### `olympus`

| File | Bugs fixed |
|---|---|
| `matchmaking_service/matchmaking.go` | M1–M4, M6–M14, M17 |
| `matchmaking_service/main.go` | M5, M15 |
| `matchmaking_service/metrics.go` | M16 |
| `game_server_service/gameServer.go` | G1, G2, G5–G10, G12, G15, G20 |
| `game_server_service/randomizedMatch.go` | G3, G4, G16, G17, G18 |
| `game_server_service/main.go` | G10, G15, G19 |
| `game_server_service/metrics.go` | G14 |
| `platform_service/platform.go` | P1–P7, P10 |
| `platform_service/main.go` | P8 |
| `platform_service/metrics.go` | P9 |
| `dbScripts/init.sql` | I1, G13, I5 |
| `docker-compose.yaml` | I1–I4 |
| `testing-docker-compose.yaml` | I1, I2, I4 |

### `protobufClient`

| File | Bugs fixed |
|---|---|
| `profiles.go` | C1–C5, C7–C11 |
| `randomizedMatch.go` | C6 |

---

## 17. Addendum — unbounded match history (found during a long live run)

This one did not show up in the 3-minute validation runs. It appears once the same
10,000 players have been cycling for **20–30 minutes**.

### Symptom

```
player E1eOOPaQID: matchHistory: Post "http://localhost:8082/matchHistory":
    context deadline exceeded
platform_server | Error iterating through rows: timeout: context deadline exceeded
postgres        | canceling statement due to user request
postgres        | could not send data to client: Connection reset by peer
```

### Root cause

`/matchHistory` selected **every** row for a PUUID:

```go
SELECT ... FROM "matchHistory" WHERE "puuid" = $1
```

There is no `ORDER BY` and no `LIMIT`. The response therefore grows linearly with how
long the run has been going. Measured on the live database after ~25 minutes:

| Metric | Value |
|---|---|
| Total rows | 1,490,040 |
| Distinct PUUIDs | 10,001 |
| Matches per PUUID | 147–157 |
| Bytes for one player's history | **1,083,953 (~1.06 MB)** |
| Heap blocks read per query | **162** (145 from disk) |
| 10k concurrent queries | ~10 GB of rows per wave |

Every player calls `/matchHistory` after every game, so at 10k users the platform
service was reading and JSON-decoding gigabytes per wave. The 5 s `dbOpTimeout`
expired mid-stream, and the client's 15 s timeout expired before the handler could
write a response.

The earlier fix (`idx_match_history_puuid`) only made the lookup *possible*; it still
had to read and sort the player's entire history.

### Fix

**P10 — bound the query** (`platform_service/platform.go`):

```go
const matchHistoryLimit = 20

rows, err := dbPool.Query(ctx,
    `SELECT ... FROM "matchHistory" WHERE "puuid" = $1
     ORDER BY "gameEndTimestamp" DESC, "matchID" DESC
     LIMIT $2`,
    unpackedRequest.PlayerPUUID, matchHistoryLimit)
```

Also fixed the incomplete P3: a mid-stream `rows.Err()` now returns `500` instead of
silently sending a partial `200`.

**I5 — matching composite index** (`dbScripts/init.sql`):

```sql
CREATE INDEX IF NOT EXISTS idx_match_history_puuid_recent
    ON "matchHistory" ("puuid", "gameEndTimestamp" DESC, "matchID" DESC);
```

The index matches the `ORDER BY`, so Postgres stops after 20 heap reads instead of
reading the whole history and sorting it.

### Verified on the live database

| | Before | After |
|---|---|---|
| Heap blocks read | 162 | **20** |
| Execution time | 0.418 ms | **0.044 ms** |
| Rows returned | 157 | **20** |
| Response size | ~1.06 MB | **~135 KB** |

This makes the per-request cost **constant** for the life of the run instead of growing
with it. Note that the fix was applied to the live database with
`CREATE INDEX CONCURRENTLY`, and the platform service must be rebuilt/restarted for the
new query to take effect.

---

## 18. Follow-up — how P10 was found and where the fix stands

This section was added after the original post-mortem. It explains why a bug this large
was not in the first document, why the earlier validation did not catch it, and exactly
what has and has not been deployed.

### Why the 3-minute validation passed

The bug is **latent**: its cost depends on how long the run has been going, not on how
many users are connected.

| Run length | Matches per player | Rows returned by `/matchHistory` | Approx. response |
|---|---|---|---|
| 3 minutes | ~30 | ~30 | ~200 KB |
| 25 minutes | ~150 | ~150 | **~1.06 MB** |

At three minutes the endpoint was merely expensive. At twenty-five minutes it was
returning a megabyte per player, ten thousand times per cycle. The original validation
was correct for what it measured, but it did not run long enough to expose a cost that
grows with time. **A load test that passes for three minutes does not prove stability
for thirty.**

### What the error lines actually meant

| Log line | What it was |
|---|---|
| `platform_server: Error iterating through rows: timeout: context deadline exceeded` | `rows.Err()` after the 5 s `dbOpTimeout` fired mid-stream. The old code logged it and still sent a partial `200`. |
| `postgres: canceling statement due to user request` | pgx cancelled the query when the context deadline expired. |
| `postgres: could not send data to client: Connection reset by peer` | The platform service had already given up on the connection. |
| `player ...: matchHistory: ... context deadline exceeded` | The client's own 15 s `apiTimeout` expired before the handler could finish. |

Those four lines are one event seen from four places: a query that had grown too large
to finish inside the deadline.

### The feedback loop

1. The run gets longer, so each player has more history.
2. Each `/matchHistory` reads more rows and decodes more JSON.
3. Queries exceed 5 s, so handlers fail; clients exceed 15 s and retry.
4. Retries add load to a service that is already CPU- and I/O-bound.
5. The table keeps growing while all of this happens, making step 2 worse.

The `LIMIT` breaks the loop at step 2: the cost stops depending on run length.

### What was changed

- **P10** — `platform_service/platform.go` now returns only the 20 most recent matches
  (`ORDER BY "gameEndTimestamp" DESC, "matchID" DESC LIMIT 20`), and a mid-stream
  `rows.Err()` returns `500` instead of a partial `200`.
- **I5** — `dbScripts/init.sql` now defines the matching composite index
  `(puuid, gameEndTimestamp DESC, matchID DESC)`.

See section 17 for the code and measurements.

### Deployment status

| Item | Status |
|---|---|
| `platform_service/platform.go` | **Changed in the working tree**; builds and vets cleanly |
| `dbScripts/init.sql` | **Changed in the working tree** |
| Live Postgres index | **Created** with `CREATE INDEX CONCURRENTLY` (2.5 s, 189 MB) and persisted in the `postgres_data` volume |
| Running `platform_server` container | **Not rebuilt** — still runs the old query |
| `BUGS.md` | Updated with P10, I5, and this follow-up |
| Git | Nothing committed or pushed |

The code fix is on disk and the database index is live, but the endpoint will keep
behaving the old way until the platform image is rebuilt:

```bash
cd /home/sawa/devenv/olympus
sudo docker compose up -d --build platform_server
```

### How to confirm the fix is live

After rebuilding, run the client and check for the absence of the old errors:

```bash
cd /home/sawa/devenv/protobufClient
go run . -users 10000
```

- No `Error iterating through rows: timeout` in `platform_server` logs.
- No `matchHistory: ... context deadline exceeded` in the client.
- The same 10k players can run for 30+ minutes without the history query getting slower.

To confirm the query plan uses the new index:

```bash
sudo docker compose exec -T postgres psql -U sawa -d olympus -c \
  "EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM \"matchHistory\" \
   WHERE \"puuid\" = (SELECT puuid FROM \"matchHistory\" LIMIT 1) \
   ORDER BY \"gameEndTimestamp\" DESC, \"matchID\" DESC LIMIT 20;"
```

You want to see `Index Scan using idx_match_history_puuid_recent` and roughly 20 heap
reads, not a bitmap scan over the player's entire history.

### Notes

- The 1.49M existing rows belong to the **previous** run's randomly generated PUUIDs.
  A new run creates new PUUIDs, so it starts with an empty history and is not slowed by
  the old data. Truncating is optional, for disk space only.
- The composite index is already in the live volume, so no manual SQL is needed now.
  `init.sql` only runs when the volume is first created; it will recreate the index
  automatically on a fresh volume.

## 19. Follow-up — 300-second games and the "Players In Game" gauge

> **Update:** the fixed 300 s duration described in this section was later replaced by a
> per-match duration of 200–400 s (see section 21). The watchdog and gauge fixes are
> unchanged and still apply.

Two related changes were made after the 10k run: the simulated game was lengthened
to **300 seconds**, and the `game_server_queueup_active_players` gauge was moved off
the short-lived HTTP handler. They are described together because they only make
sense as a pair.

### 19.1 Why the gauge read 0

`/connectToMatch` used to increment the gauge around the HTTP handler:

```go
http.HandleFunc("/connectToMatch", ... {
    activeConnections.Inc()
    defer activeConnections.Dec()
    NewPlayerConnection(...)
})
```

`NewPlayerConnection` registers the player and returns immediately; the match work
happens in a background goroutine. The gauge was therefore non-zero for only a few
milliseconds, while Prometheus scrapes every 15 seconds. It almost always sampled
`0` even though `game_server_api_requests_total{endpoint="/connectToMatch"}` was in
the millions.

**Fix:** increment/decrement inside the per-player match goroutine in
`NewPlayerConnection` (`gameServer.go`). A player is now counted from registration
until its match work (game + database writes) finishes.

### 19.2 The 300-second game

The game server used to simulate a **250 ms** game while the client slept **5–6 s**.
The two were unrelated, so the gauge could never reflect the client's game and a
long server-side sleep on its own would have been unsafe. Three timing rules now
line up:

| Phase | Duration | Where |
|---|---|---|
| Wait for all 10 participants | up to 2 min (`matchConnectTimeout`) | `ConnectPlayerToMatch` |
| Simulated game | 300 s (`simulatedGameDuration`) | `ConnectPlayerToMatch` |
| Result written to Postgres | immediately after the game | `NewPlayerConnection` goroutine |
| Client waits before reading | 300 s + 15 s (`postGameBuffer`) | `runPlayerCycle` |

Files changed:

- `game_server_service/gameServer.go`
  - added `simulatedGameDuration = 300 * time.Second`
  - added `matchCleanupGrace = 2 * time.Minute`
  - replaced the `250 * time.Millisecond` game sleep with `simulatedGameDuration`
  - when the match becomes full, `matchTracker.arrive` resets the watchdog from
    `matchConnectTimeout` to `simulatedGameDuration + matchCleanupGrace`
- `game_server_service/randomizedMatch.go`
  - `GameDuration` is now exactly `simulatedGameDuration` (300 s) instead of a
    random 60–359 s
  - `gameEndUnixTime` is `now + simulatedGameDuration`, so `GameStartTime` and
    `GameEndTime` describe the real game window
- `protobufClient/profiles.go`
  - added `simulatedGameDuration = 300 * time.Second` and
    `postGameBuffer = 15 * time.Second`
  - replaced the 5–6 s client sleep with `simulatedGameDuration + postGameBuffer`

### 19.3 Why the watchdog had to change

`matchConnectTimeout` (2 min) was doing double duty: the participant-wait deadline
**and** the cleanup watchdog. A 300 s game would have been deleted from the global
maps at t = 120 s, so every player's `matchDataMap.Load` at t = 300 s would fail and
**no rows would be written to `matchHistory`** — a silent failure, because the client
does not validate the returned history.

The two concerns are now separate:

- the wait loop still gives up after `matchConnectTimeout`;
- once the match is full, the watchdog is reset to `simulatedGameDuration + matchCleanupGrace`,
  which covers the game and the database writes;
- the normal completion path still stops the timer after `tracker.wg.Wait()` and
  runs `cleanup` exactly once.

A match that never fills still expires after 2 minutes, and `matchHeartBeat` remains
the hourly safety net.

### 19.4 Why the client waits longer than the game

The client's sleep starts when `/connectToMatch` returns, but the server's 300 s sleep
starts when the **tenth** participant has registered. The matchmaker hands the same
match to all ten players at once, so the gap is normally milliseconds; the 15 s
`postGameBuffer` absorbs that plus the database writes. The result is that the client
reads `/matchHistory` only after the server has committed the row.

### 19.5 Expected load profile

- Cycle time per player: ~315 s (300 s game + buffer + short API calls).
- Match rate: ~10,000 players ÷ 10 per match ÷ 315 s ≈ **3 matches/s** sustained
  (bursty at the start and whenever a wave of games ends).
- `game_server_queueup_active_players` should sit near **10,000** during steady state,
  not 0 and not 500,000.
- Goroutines and map entries stay bounded because each player is in exactly one match
  at a time. The old client re-queued every ~6 s; it no longer does.

### 19.6 How to apply and verify

```bash
# game server (Docker)
cd /home/sawa/devenv/olympus
sudo docker compose up -d --build game_server

# client
cd /home/sawa/devenv/protobufClient
go run . -users 10000
```

Verify during the run:

```bash
# should hover around 10000
watch -n5 'curl -s localhost:8081/metrics | grep ^game_server_queueup_active_players'

# a match appears only after ~300 s
sudo docker compose exec -T postgres psql -U sawa -d olympus -c \
  "SELECT count(*) FROM \"matchHistory\";"
```

After ~5 minutes the count should start growing by roughly 10 rows per match, and
`gameDuration` should read `300` for the new rows.

### 19.7 Notes

- These edits are **uncommitted** and nothing has been pushed.
- `go build` and `go vet` pass in both repositories.
- The 300 s duration is a load-testing choice, not a gameplay feature. Lower
  `simulatedGameDuration` in both repositories together if a shorter cycle is wanted;
  changing only one side reintroduces the client/server timing gap.

---

## 20. Post-mortem — the 300-second game change

> **Update:** the duration described here was later made per-match (200–400 s); see
> section 21. The root-cause analysis and the watchdog/gauge fixes still apply.

Section 19 describes *what* changed. This section is the retrospective: why the change
was needed, what would have broken if the sleep had simply been edited, what the real
root causes were, and what is still risky.

### 20.1 The request

> "I want the simulated games to be 300 seconds."

That sounds like a one-line change. It was not, because **the simulated game was never
one clock**. Three different durations described the same event, and none of them
matched:

| Clock | Before | Owner |
|---|---|---|
| Server "game" | `250 ms` | `ConnectPlayerToMatch` |
| Client "game" | `5–6 s` | `runPlayerCycle` |
| Match metadata (`gameDuration`) | random `60–359 s` | `generateRandomMatchData` |

The client and server each slept independently, and the metadata described a third,
imaginary game. There was no shared constant, no comment linking the values, and no
test that compared them. The only reason this never caused an outage is that the
server-side work after the sleep did not depend on how long the sleep was — until we
tried to make it 300 seconds.

### 20.2 What would have happened with only the sleep changed

If `time.Sleep(250 * time.Millisecond)` had been changed to `time.Sleep(300 * time.Second)`
and nothing else, the system would have failed in three independent ways. The first is
the dangerous one because it is **silent**.

**Failure 1 — the watchdog deletes the match before the game ends.**

`matchConnectTimeout = 2 * time.Minute` was doing two unrelated jobs:

```go
// participant-wait deadline
deadline := time.Now().Add(matchConnectTimeout)

// cleanup watchdog
t.timer = time.AfterFunc(matchConnectTimeout, func() {
    t.cleanOnce.Do(cleanup)
})
```

With a 300 s sleep:

| Time | Event |
|---|---|
| t = 0 | All 10 players connect; goroutines start the 300 s sleep |
| t = 120 s | Watchdog fires and deletes `activeMatches`, `matchDataMap`, `matchParticipantsMap`, `matchCreationDates`, `waitGroupMap` |
| t = 300 s | Goroutines wake, `matchDataMap.Load(matchID)` returns `!ok`, and they return **before any DB write** |

Result: **zero `matchHistory` inserts and zero `UpdateProfile` calls**. The client does
not validate how many matches it received, so `/matchHistory` still returns `200` with
an empty or stale list. The run looks healthy while the database stays empty. This is
the worst class of load-test bug: a green dashboard over a dead pipeline.

**Failure 2 — the client re-queues every ~6 seconds.**

`/connectToMatch` responds as soon as the player is registered; the server's sleep is in
a background goroutine. Changing only the server therefore does not make the client
wait. Each player would keep cycling every 5–6 s while its previous 300 s server
goroutine was still alive, so each player would be in roughly 40–50 matches at once:

```
~170 matches/s × 10 players × 300 s ≈ 500,000 in-flight player goroutines
```

The fixed gauge would then report **in-flight match participations**, not players —
ramping toward ~500,000 instead of 10,000. The metric would be "correct" for what it
measured and useless for what the chart claimed.

**Failure 3 — unbounded growth in the maps.**

Even with the 2-minute watchdog, ~170 matches/s × 120 s ≈ 20,000 match entries would be
retained, each holding participants, generated results, and JSON. Memory would climb
through the whole run, and the eventual cleanup wave would add GC pressure on top of
the already-bursty database writes.

### 20.3 Root causes

Three design mistakes, not one coding mistake:

1. **Timing was implicit and duplicated.** The same logical duration lived in two
   repositories with no shared source of truth and no cross-reference in comments.
2. **One timeout served two phases.** `matchConnectTimeout` was both "how long to wait
   for players" and "how long the match may exist." Those are different questions with
   different answers, and a single value cannot satisfy both once games are long.
3. **The metric measured the wrong lifetime.** `game_server_queueup_active_players` was
   incremented around the HTTP handler, but the handler returns immediately after
   registration. It was measuring request duration (milliseconds), not game duration
   (seconds), which is why it read `0` even at 10k users and millions of requests.

### 20.4 The fix

**One duration constant per repository.** The client and server are separate Go modules,
so the value cannot literally be shared. It is declared once on each side and documented
as a cross-repo contract:

```go
// game_server_service/gameServer.go
simulatedGameDuration = 300 * time.Second
matchCleanupGrace     = 2 * time.Minute

// protobufClient/profiles.go
simulatedGameDuration = 300 * time.Second
postGameBuffer        = 15 * time.Second
```

**Separate the two phases of the watchdog.** The timer still starts at
`matchConnectTimeout` so an unfilled match is reaped after 2 minutes. When the tenth
player arrives, `matchTracker.arrive` resets it to cover the game plus the writes:

```go
if t.arrived == t.expected {
    t.timer.Reset(simulatedGameDuration + matchCleanupGrace)
    t.doneOnce.Do(func() { close(t.done) })
    return true, false
}
```

The normal path is unchanged: the last handler waits on `tracker.wg`, stops the timer,
and runs `cleanup` exactly once. A match that never fills still expires after 2 minutes,
and `matchHeartBeat` remains the hourly safety net.

**Align the metadata with reality.** `GameDuration` is now exactly 300, and
`gameEndUnixTime` is `now + 300 s`, so `GameStartTime`/`GameEndTime` describe the window
the server actually simulated instead of a random 60–359 s game.

**Wait long enough on the client.** The client sleeps `300 s + 15 s` so its
`/matchHistory` call happens after the server has committed the row.

**Move the gauge to the match lifetime.** `activeConnections.Inc()`/`Dec()` now bracket
the per-player match goroutine in `NewPlayerConnection`, so the metric measures players
in a game rather than milliseconds of handler time.

### 20.5 The remaining race, and why 15 seconds is enough

The server's 300 s starts when the **tenth** participant registers; each client's 300 s
starts when its own `/connectToMatch` returns. Those are not the same instant:

- If the match fills instantly, the gap is milliseconds.
- If the match fills slowly, the first player's client can finish its wait before the
  server's game ends.

The matchmaker forms a full 10-player match and then hands the same `matchID` to all ten
players at once, so in normal operation the gap is tiny. `postGameBuffer = 15 s` covers
that gap plus the database writes. It is a **buffer, not a guarantee**: a pathological
slow fill or a very slow disk could still let a client read before its row exists. The
correct long-term fix is an explicit readiness signal (see 20.8), not a larger sleep.

### 20.6 Load profile: what 300 s games actually change

| Metric | 5–6 s games (before) | 300 s games (after) |
|---|---|---|
| Player cycle | ~6 s | ~315 s |
| Match rate | ~170 /s | **~3.2 /s** |
| `matchHistory` inserts | ~1,700 /s | **~32 /s** |
| Concurrent matches | ~1,000 | ~1,000 |
| Player goroutines | ~10k | ~10k |
| Gauge reading | `0` | **~9.5k–10k** |
| Match metadata duration | random 60–359 s | **300 s** |

The player count is unchanged; the **churn** drops by roughly 50×. That is the point of
the change: 10,000 players stay connected but spend most of their time in a long game
instead of hammering matchmaking, the game server, and Postgres every few seconds.

Two consequences are worth stating explicitly:

- **The gauge reads ~9.5k, not exactly 10k**, because each 315 s cycle contains ~15 s of
  post-game work during which the player is no longer counted. That is accurate, not a
  bug.
- **Games end in waves.** The initial 10k players start in a 2-second stagger, so their
  games end in a 2-second burst, producing a burst of ~10k inserts. The database pool
  and the 20-row history limit absorb it, but the write pattern is spikier than the
  steady-state average suggests.

### 20.7 Verification status

| Check | Result |
|---|---|
| `gofmt -l` (both repos) | clean |
| `go build ./...` (both repos) | pass |
| `go vet ./...` (both repos) | pass |
| Cross-repo duration values | both `300 * time.Second` |
| Old short-game references (`250ms`, `5, 6`, `Intn(300)+60`) | none remain |
| Live 10k run | **not yet run** — requires rebuilding `game_server` in Docker |

The live checks to run after the rebuild are in section 19.6: the gauge should climb
toward 10k, and new `matchHistory` rows should appear only after ~300 s with
`gameDuration = 300`.

### 20.8 Lessons

1. **A sleep is not a contract.** When two processes must agree on a duration, write it
   down in both places, name it identically, and comment on the coupling. A shared
   config value would be better than a shared comment.
2. **Never let one timeout serve two phases.** "Wait for participants" and "retain the
   match" have different failure modes. Splitting them is what made a 300 s game safe.
3. **Metrics must measure the thing they are named after.** A gauge around a handler
   measures handler latency, not user state. Always increment at the boundary of the
   state being counted.
4. **Silent success is worse than an error.** The server-only change would have produced
   no errors and no rows. Load tests should assert on *effects* (rows written, matches
   completed), not just on HTTP status codes.
5. **A passing test is only valid for the shape it tested.** The previous validation
   proved 5–6 s games at 10k users. It said nothing about 300 s games, because the
   duration changes the match rate, the goroutine lifetime, and the DB write pattern.
6. **Changing one side of a distributed protocol is not a change; it is a new protocol.**
   Server and client had to move together.

### 20.9 Residual risks and follow-ups

| Risk | Impact | Suggested follow-up |
|---|---|---|
| Duration duplicated across two repos | Drift if only one side is edited | Expose it from the server (e.g. `/health` or the connect response) and have the client read it |
| Fixed `postGameBuffer` | Client can read before a slow DB write | Poll `/matchHistory` for the expected `matchID` with a deadline instead of sleeping |
| Synchronized game-end waves | Insert bursts at t≈300 s and on each cycle | Add per-player jitter to the client's wait |
| No timing regression test | Future edit can desynchronize the clocks again | Make the duration overridable (env/flag) and add a short-duration integration test |
| Gauge counts participants, not unique players | A bug that double-registers a player would inflate it | Keep the duplicate-PUUID rejection; consider a labeled per-match count |

### 20.10 File change index for this change

| File | Change |
|---|---|
| `game_server_service/gameServer.go` | `simulatedGameDuration`, `matchCleanupGrace`, 300 s game sleep, watchdog reset on full, gauge moved to match goroutine |
| `game_server_service/randomizedMatch.go` | `gameDuration = 300`, `gameEndUnixTime = now + 300 s` |
| `game_server_service/main.go` | gauge removed from the HTTP handler |
| `game_server_service/metrics.go` | gauge help text corrected |
| `protobufClient/profiles.go` | `simulatedGameDuration`, `postGameBuffer`, 315 s wait |
| `BUGS.md` | sections 19 and 20 |

Everything above is in the working tree and **uncommitted**; nothing has been pushed.

---

## 21. Revision — per-match game length (200–400 s)

> This section supersedes the fixed 300 s duration in sections 19 and 20. It does not
> change the watchdog split, the gauge fix, or the client buffer.

### 21.1 Why vary the length

A fixed 300 s duration fixes more than the game length: it synchronizes the whole run.
All 1,000 concurrent matches start within the client's 2 s initial stagger, so they all
end within that same 2 s window. That produces:

- a **queue burst** of ~10,000 players returning to matchmaking at once;
- a **write burst** of ~10,000 `matchHistory` inserts and profile updates at once;
- a **sawtooth** in the "Players in match" gauge as whole waves leave and re-enter;
- repeated re-matching of the same ten players, because their cycles stay aligned.

Giving each match its own length spreads those events over a 200 s window. The average
load is unchanged; the peaks are much lower.

### 21.2 How the two sides agree on a match's length

The duration must be **per match**, not per player: all ten players in a match must wait
for the same game, and the server must write the result at the same time. It also must be
known to both the game server and the client.

Rather than add a protobuf field and regenerate the stubs, both sides derive the duration
from the one value they already share: the match ID. Match IDs are `MATCH_` plus ten
random base-36 characters, generated once by the matchmaker and sent unchanged to the game
server and to all ten players.

```go
// Identical implementation in olympus/game_server_service/gameServer.go and
// protobufClient/profiles.go.
func matchGameDuration(matchID string) time.Duration {
    h := fnv.New64a()
    _, _ = h.Write([]byte(matchID))

    spanSeconds := int64((maxGameDuration - minGameDuration) / time.Second)
    offsetSeconds := int64(h.Sum64() % uint64(spanSeconds+1))
    return minGameDuration + time.Duration(offsetSeconds)*time.Second
}
```

with `minGameDuration = 200 * time.Second` and `maxGameDuration = 400 * time.Second`.

Properties:

- **Deterministic** — the same ID always gives the same duration, on both sides.
- **Uniform** — a 100,000-ID check produced min 200 s, max 400 s, and five 40 s buckets
  within 2% of each other.
- **No new state** — nothing is stored to remember the duration; it is recomputed from
  the ID.
- **No protocol change** — the protos are untouched and no extra round trip is needed.

The alternative (the matchmaker picks a duration and includes it in `MatchCreation` /
`MatchResponse`) would be more explicit but requires editing and regenerating the
protobuf stubs. The hash approach keeps the wire format unchanged.

### 21.3 What changed

| File | Change |
|---|---|
| `game_server_service/gameServer.go` | `minGameDuration`/`maxGameDuration`, `matchGameDuration()`, per-match sleep, tracker stores the match's duration, watchdog reset uses it |
| `game_server_service/randomizedMatch.go` | `gameDuration` and `gameEndTime` use the per-match duration |
| `protobufClient/profiles.go` | identical `matchGameDuration()` and bounds, sleep is `matchGameDuration(matchID) + postGameBuffer` |
| `BUGS.md` | this section and the superseded notes in 19 and 20 |

The metadata is now internally consistent per match: `gameDuration` is the same value the
server slept for, and `gameStartTime`/`gameEndTime` bracket that window.

### 21.4 Load profile

| Metric | Fixed 300 s | Per-match 200–400 s |
|---|---|---|
| Average game | 300 s | 300 s |
| Game lengths | 1 value | 201 values, uniform |
| Match ends | one wave every ~315 s | spread over ~200 s |
| Average match rate | ~3.2 /s | ~3.2 /s |
| Peak insert rate | ~10k in a 2 s burst | smoothed across the window |
| Gauge | sawtooth near 10k | smoother, near 10k |
| Total work | unchanged | unchanged |

The average is deliberately kept at 300 s, so the change is about **variance, not
throughput**. If a different mean is wanted, shift both bounds together.

### 21.5 Invariants and risks

- **The two helpers must stay identical.** Hash algorithm, bounds, inclusive range, and
  the `+1` in the modulus are a cross-repo contract. If one side changes and the other
  does not, clients will read before or after the server writes, and the gauge will
  drift from the real player count. The comment in both files says so.
- **`postGameBuffer` is still 15 s.** It covers the gap between the client's connect
  response and the server's "match full" moment, plus the database writes. Variance does
  not change that reasoning.
- **Grouped re-matching remains possible.** Players who finish together may be matched
  together again, but their next game gets a new ID and therefore a new length, so the
  groups decorrelate over successive games.
- **No live run yet.** `gofmt`, `go build`, and `go vet` pass in both repositories; the
  10k run still requires rebuilding `game_server`.

### 21.6 How to verify the variance

After rebuilding and running the client, confirm that durations are spread rather than
constant:

```bash
sudo docker compose exec -T postgres psql -U sawa -d olympus -c \
  "SELECT \"gameDuration\", count(*) FROM \"matchHistory\"
   GROUP BY \"gameDuration\" ORDER BY \"gameDuration\"::int LIMIT 10;"
```

New rows should show many distinct values in the 200–400 range, not a single `300`. The
gauge should also rise and fall more smoothly than with the fixed duration.

---

*Generated from the actual working-tree diffs of both repositories. Nothing in this
document has been committed or pushed.*
