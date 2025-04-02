package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/redis/go-redis/v9"
)

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

// Define Prometheus metrics
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "matchmaking_api_requests_total",
			Help: "Total number of requests to API endpoints",
		},
		[]string{"endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "matchmaking_api_request_duration_seconds",
			Help:    "Histogram of response time for API requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "matchmaking_queueup_active_players",
			Help: "Number of players currently connected to /queueUp",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, activeConnections)
}

func instrumentedHandler(endpoint string, handler func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		handler(w, r)
		duration := time.Since(start).Seconds()

		requestsTotal.WithLabelValues(endpoint).Inc()
		requestDuration.WithLabelValues(endpoint).Observe(duration)
	}
}

func main() {
	var mu sync.Mutex
	var partyResourcesMap sync.Map
	ctx := context.Background()
	var redisAddr string

	if IsRunningInDocker() {
		redisAddr = "matchmaking_redis:6379"
	} else {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	// Metrics endpoint Prometheus uses to scrape data from matchmaking service
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/queueUp", instrumentedHandler("/queueUp", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()
		defer r.Context().Done()

		unpackedRequest := UnpackRequest(w, r)
		if !UnpackedRequestValidation(unpackedRequest) {
			http.Error(w, "Missing requried data in payload", http.StatusBadRequest)
			return
		}

		dsn := "postgres://sawa:sawa@postgres:5432/olympus"
		conn, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			log.Fatalf("Unable to connect to database: %v\n", err)
		}
		defer conn.Close(context.Background())

		// Validates with the summonerRankedInfo database and uses that value for the user if it exists
		// If not then just use the one provided
		var myRank int
		err = conn.QueryRow(context.Background(),
			`SELECT rank FROM "summonerRankedInfo" WHERE puuid = $1`, unpackedRequest.PlayerPuuid).
			Scan(&myRank)
		if err == pgx.ErrNoRows {
			// defaults: rank=22 wins=0, losses=0
			myRank = int(unpackedRequest.PlayerRank)
		} else if err != nil && err != pgx.ErrNoRows {
			log.Fatal("Failed to fetch summoner rank info:", err)
		}

		AddPartyToRedis(w, unpackedRequest, myRank, rdb, ctx)

		matchmakingContext, cancel := context.WithCancel(context.Background())
		partyResourcesMap.Store(unpackedRequest.PartyId, PartyResources{
			CancelFunc: cancel,
			Writer:     w,
		})

		MatchFinder(w, unpackedRequest, rdb, ctx, &partyResourcesMap, matchmakingContext, r, &mu)
	}))

	http.HandleFunc("/health", instrumentedHandler("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	port := ":8080"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
