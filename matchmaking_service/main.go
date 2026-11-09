package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initDB()

	var redisAddr string
	if IsRunningInDocker() {
		redisAddr = "matchmaking_redis:6379"
	} else {
		redisAddr = "localhost:6379"
	}

	// The default pool is 10 * GOMAXPROCS, which is far too small for 10k
	// simulated players. Bound every operation so a slow Redis cannot pin
	// handler goroutines forever.
	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		Password:     "",
		DB:           0,
		PoolSize:     200,
		MinIdleConns: 20,
		PoolTimeout:  5 * time.Second,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	defer rdb.Close()

	// One background worker owns matchmaking. Players no longer poll.
	mm := NewMatchmaker(ctx, rdb)
	mm.Start()

	// Metrics endpoint Prometheus uses to scrape data from matchmaking service
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/queueUp", instrumentedHandler("/queueUp", func(w http.ResponseWriter, r *http.Request) {
		QueueUp(w, r, mm, rdb, ctx)
	}))

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	port := ":8080"
	PrintBanner(port)

	// /queueUp is a long-lived streaming endpoint: a player can legitimately
	// wait a long time for a match, so WriteTimeout must stay disabled. The
	// request context still closes the connection as soon as the client leaves.
	srv := &http.Server{
		Addr:              port,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Fatal(srv.ListenAndServe())
}
