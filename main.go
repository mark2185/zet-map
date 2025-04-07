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

type Vehicle struct {
	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`
	RouteID   string  `json:"route_id"`
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

var Routes = map[RouteID]Trips{}

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
	return Routes[route_id][trip_id]
}

var allVehicles atomic.Value
var lastUpdateTimestamp uint64 = 0

func updateVehiclesPosition() {
	for {
		feed, err := fetchGTFSRealTime(gtfsURL)
		if err != nil {
			log.Printf("Error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if feed.Header.Timestamp != nil && *feed.Header.Timestamp <= lastUpdateTimestamp {
			log.Printf("Feed last updated: %v, local data age: %v, not refreshing data\n", *feed.Header.Timestamp, lastUpdateTimestamp)
			time.Sleep(2 * time.Second)
			continue
		}

		lastUpdateTimestamp = *feed.Header.Timestamp

		vehicles := []Vehicle{}
		for _, entity := range feed.Entity {
			if entity.Vehicle != nil {
				routeID := RouteID(*entity.Vehicle.Trip.RouteId)
				tripID := TripID(*entity.Vehicle.Trip.TripId)
				trip := getTrip(routeID, tripID)
				directionIcon := ">"
				if trip.Direction != "0" {
					directionIcon = "<"
				}
				vehicles = append(vehicles, Vehicle{
					Latitude:  *entity.Vehicle.Position.Latitude,
					Longitude: *entity.Vehicle.Position.Longitude,
					RouteID:   *entity.Vehicle.Trip.RouteId,
					Headsign:  trip.Headsign,
					Direction: directionIcon,
				})
			}
		}
		allVehicles.Store(vehicles)
	}
}

func vehicleHandler(w http.ResponseWriter, r *http.Request) {

	response := struct {
		LastUpdated uint64    `json:"last_updated"`
		Vehicles    []Vehicle `json:"vehicles"`
	}{
		LastUpdated: lastUpdateTimestamp,
		Vehicles:    allVehicles.Load().([]Vehicle),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func mapHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
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
		if _, exists := Routes[routeID]; !exists {
			Routes[routeID] = Trips{}
		}

		tripID := TripID(row[2])
		Routes[routeID][tripID] = Trip{Headsign: row[3], Direction: row[5]}
	}
}

func main() {
	loadTripsData()

	go updateVehiclesPosition()
	time.Sleep(2 * time.Second)

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicles", vehicleHandler)

	log.Println("Server running on port 8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))

	// if feed.Header.Timestamp != nil {
	// fmt.Printf("Feed last updated: %v\n", *feed.Header.Timestamp)
	// }
}
