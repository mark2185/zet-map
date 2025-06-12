package main

import (
	"log"
	"net/http"

	"github.com/mark2185/zet/pkg/zet"
)

func main() {
	if err := zet.FetchInitialData(); err != nil {
		log.Fatalf("Failed to load initial data: %v", err)
	}

	go zet.FetchDataLoop()

	http.HandleFunc("/", zet.MapHandler)
	http.HandleFunc("/favicon.ico", zet.FaviconHandler)
	http.HandleFunc("/stops", zet.StopsHandler)
	http.HandleFunc("/events", zet.SseHandler)

	log.Println("Server running on port 8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
