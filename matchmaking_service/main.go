package main

import (
	"context"
	"log"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/redis/go-redis/v9"
)

func main() {
	var mu sync.Mutex
	var partyResourcesMap sync.Map
	ctx := context.Background()
	var redisAddr string
	initDB()

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
		QueueUp(w, r, ctx, &mu, &partyResourcesMap, rdb)
	}))

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := ":8080"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
