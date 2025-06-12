package zet

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

func FaviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "data/favicon-32x32.png")
}

func StopsHandler(w http.ResponseWriter, r *http.Request) {
	type Stop struct {
		Latitude  float32 `json:"lat"`
		Longitude float32 `json:"lon"`
		Headsign  string  `json:"headsign"`
	}

	stopsFile, err := os.Open("./data/stops.txt")
	if err != nil {
		log.Println("Cannot read stops file!")
		return
	}
	defer stopsFile.Close()

	stopsReader := csv.NewReader(stopsFile)

	allStops, _ := stopsReader.ReadAll()

	stops := []Stop{}

	for _, stop := range allStops {
		log.Println(stop)
		location_type := stop[8]
		platform := "0"
		if location_type != platform {
			continue
		}
		lat, _ := strconv.ParseFloat(stop[4], 32)
		lon, _ := strconv.ParseFloat(stop[5], 32)
		stops = append(stops, Stop{
			Latitude:  float32(lat),
			Longitude: float32(lon),
			Headsign:  stop[1],
		})
	}

	// stops = []Stop{
	// Stop{Latitude: 45.794911, Longitude: 15.959232, Headsign: "Vjesnik 1"},
	// Stop{Latitude: 45.793392, Longitude: 15.958070, Headsign: "Vjesnik 2"},
	// Stop{Latitude: 45.794151, Longitude: 15.958651, Headsign: "Vjesnik"},
	// }

	response := struct {
		Stops []Stop `json:"stops"`
	}{
		Stops: stops,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func MapHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func SseHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("SSE client connected")

	// Setup headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // optional

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	clientLastUpdate := uint64(0)

	// Keep the connection alive and send updates
	for {
		current := atomic.LoadUint64(&lastUpdateTimestamp)
		if current > clientLastUpdate {
			clientLastUpdate = current

			vehicles := allVehicles.Load().(map[RouteID]Vehicles)
			data, _ := json.Marshal(vehicles)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		time.Sleep(1 * time.Second)

		// Optional: detect client disconnect
		if r.Context().Err() != nil {
			log.Println("SSE client disconnected")
			return
		}
	}
}

