package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/noahpop77/Olympus/matchmaking"
	"github.com/noahpop77/Olympus/matchmaking/party"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/redis/go-redis/v9"
)

func PrintBanner(port string) {
	fmt.Println(`================================
▗▖  ▗▖▗▄▄▄▖▗▄▄▖  ▗▄▖  ▗▄▄▖▗▖ ▗▖
▐▛▚▞▜▌  █  ▐▌ ▐▌▐▌ ▐▌▐▌   ▐▌▗▞▘
▐▌  ▐▌  █  ▐▛▀▚▖▐▛▀▜▌▐▌   ▐▛▚▖ 
▐▌  ▐▌  █  ▐▌ ▐▌▐▌ ▐▌▝▚▄▄▖▐▌ ▐▌`)
	fmt.Println("================================")
	fmt.Printf("Starting server on port %s...\n", port)
	fmt.Println("================================")
}

func IsRunningInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// Define Prometheus metrics
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_requests_total",
			Help: "Total number of requests to API endpoints",
		},
		[]string{"endpoint"},
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "api_request_duration_seconds",
			Help:    "Histogram of response time for API requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "queueup_active_players",
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
		redisAddr = "redis_db:6379"
	} else {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/addToQueue", instrumentedHandler("/addToQueue", func(w http.ResponseWriter, r *http.Request) {
		var unpackedRequest party.Players
		matchmaking.UnpackRequest(w, r, &unpackedRequest)
		matchmaking.AddPartyToRedis(w, &unpackedRequest, rdb, ctx)
	}))

	http.HandleFunc("/queueUp", instrumentedHandler("/queueUp", func(w http.ResponseWriter, r *http.Request) {
		activeConnections.Inc()
		defer activeConnections.Dec()

		var unpackedRequest party.Players
		matchmaking.UnpackRequest(w, r, &unpackedRequest)

		matchmakingContext, cancel := context.WithCancel(context.Background())
		partyResourcesMap.Store(unpackedRequest.PartyId, matchmaking.PartyResources{
			CancelFunc: cancel,
			Writer:     w,
		})

		matchmaking.AddPartyToRedis(w, &unpackedRequest, rdb, ctx)
		matchmaking.MatchFinder(w, &unpackedRequest, rdb, ctx, &partyResourcesMap, matchmakingContext, r, &mu)
	}))

	port := ":8080"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}