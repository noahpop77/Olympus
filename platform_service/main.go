package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	fmt.Println("Platform Service")

	http.HandleFunc("/matchHistory", func(w http.ResponseWriter, r *http.Request) {
		getMatchHistory(w, r)
	})

	port := ":8082"
	PrintBanner(port)
	log.Fatal(http.ListenAndServe(port, nil))
}
