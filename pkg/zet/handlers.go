package zet

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

func FaviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "data/favicon-32x32.png")
}

func StopsHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Couldn't read body for stops request")
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	type Req struct {
		Routes []string `json:"routes"`
	}

	var req Req
	if err := json.Unmarshal(body, &req); err != nil {
		log.Println("Couldn't parse JSON body: ", err)
		http.Error(w, "Bad JSON", http.StatusBadRequest)
		return
	}

	log.Println("Routes: ", req.Routes)
	stops := []Stop{}
	for _, r := range req.Routes {
		stopsForRoute, exists := getStops(RouteID(r))
		if !exists {
			continue
		}
		stops = append(stops, stopsForRoute...)
	}
	// ss := []Stop{
	// {ID: "a", Latitude: 45.794911, Longitude: 15.959232, Headsign: "Vjesnik 1"},
	// {ID: "a", Latitude: 45.793392, Longitude: 15.958070, Headsign: "Vjesnik 2"},
	// {ID: "a", Latitude: 45.794151, Longitude: 15.958651, Headsign: "Vjesnik"},
	// }

	response := struct {
		S Stops `json:"stops"`
	}{
		S: stops,
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

			vehicles := allVehiclesPerRoutes.Load().(map[RouteID]Vehicles)
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
