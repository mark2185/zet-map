package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

const gtfsURL = "https://zet.hr/gtfs-rt-protobuf"

type Vehicles []Vehicle
type Vehicle struct {
	ID        string  `json:"id"`
	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`
	Headsign  string  `json:"headsign"`
	Direction string  `json:"direction"`
}

type Trip struct {
	Headsign  string
	Direction string
}

type RouteID string
type TripID string
type Trips map[TripID]Trip

type RoutesToTrips map[RouteID]Trips

var routesToTrips = RoutesToTrips{}

func fetchGTFSRealTime(url string) (*gtfs.FeedMessage, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch GTFS Realtime feed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Unexpected response code: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read response body: %v", err)
	}

	feed := &gtfs.FeedMessage{}
	if err := proto.Unmarshal(data, feed); err != nil {
		return nil, fmt.Errorf("Failed to parse protobuf: %v", err)
	}

	return feed, nil
}

func getTrip(route_id RouteID, trip_id TripID) Trip {
	return routesToTrips[route_id][trip_id]
}

var allVehicles atomic.Value
var lastUpdateTimestamp uint64 = 0



func getVehiclesData(feed *gtfs.FeedMessage) ([]*gtfs.VehiclePosition, error) {
	vehicles := []*gtfs.VehiclePosition{}
	for _, entity := range feed.Entity {
		if entity.Vehicle != nil {
			vehicles = append(vehicles, entity.Vehicle)
		}
	}
	return vehicles, nil
}

func updateGlobalRoutes(vehicles []*gtfs.VehiclePosition) {
	newVehicles := map[RouteID]Vehicles{}
	for _, v := range vehicles {
		routeID := RouteID(v.GetTrip().GetRouteId())
		if _, exists := newVehicles[routeID]; !exists {
			newVehicles[routeID] = Vehicles{}
		}
		newVehicles[routeID] = append(newVehicles[routeID], Vehicle{
			ID:        v.GetVehicle().GetId(),
			Latitude:  v.GetPosition().GetLatitude(),
			Longitude: v.GetPosition().GetLongitude(),
			Headsign:  getTrip(routeID, TripID(v.GetTrip().GetTripId())).Headsign,
		})
	}

	allVehicles.Store(newVehicles)
}
func vehicleHandler(w http.ResponseWriter, r *http.Request) {
	response := struct {
		Vehicles map[RouteID]Vehicles `json:"vehicles"`
	}{
		Vehicles: allVehicles.Load().(map[RouteID]Vehicles),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func mapHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
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

func loadTripsData() {
	tripsFile, err := os.Open("./data/trips.txt")
	if err != nil {
		log.Fatalf("Could not open trips info: %v\n", err)
	}
	defer tripsFile.Close()

	tripsReader := csv.NewReader(tripsFile)

	_, err = tripsReader.Read() // read the header
	if err != nil {
		log.Fatalf("Could not parse CSV: %v\n", err)
	}

	rawCSVdata, _ := tripsReader.ReadAll() // read the rest

	for _, row := range rawCSVdata {
		routeID := RouteID(row[0])
		if _, exists := routesToTrips[routeID]; !exists {
			routesToTrips[routeID] = Trips{}
		}

		tripID := TripID(row[2])
		routesToTrips[routeID][tripID] = Trip{Headsign: row[3], Direction: row[5]}
	}
}

func main() {
	loadTripsData()

	feed, err := fetchGTFSRealTime(gtfsURL)
	if err != nil {
		log.Fatalf("Failed to load initial data: %v", err)
	}
	if feed.Header.Timestamp != nil {
		atomic.StoreUint64(&lastUpdateTimestamp, *feed.Header.Timestamp)
	}
	vehicles, err := getVehiclesData(feed)
	if err != nil {
		log.Fatalf("Failed to load initial data: %v", err)
	}

	updateGlobalRoutes(vehicles)

	go func() {
		for {
			time.Sleep(2 * time.Second)

			feed, err := fetchGTFSRealTime(gtfsURL)
			if err != nil {
				log.Printf("Failed to fetch GTFS data: %v", err)
				continue
			}

			if feed.Header.Timestamp != nil {
				headerTimestamp := *feed.Header.Timestamp
				cachedTimestamp := atomic.LoadUint64(&lastUpdateTimestamp)
				if newDataAvailable := cachedTimestamp < headerTimestamp; !newDataAvailable {
					continue
				}
				atomic.StoreUint64(&lastUpdateTimestamp, headerTimestamp)
			}

			vehicles, err := getVehiclesData(feed)
			if err != nil {
				log.Printf("Failed to get vehicles data: %v", err)
				continue
			}

			updateGlobalRoutes(vehicles)
		}
	}()

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicles", vehicleHandler)
	http.HandleFunc("/events", sseHandler)

	log.Println("Server running on port 8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))

	// if feed.Header.Timestamp != nil {
	// fmt.Printf("Feed last updated: %v\n", *feed.Header.Timestamp)
	// }
}
