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
	Direction string  `json:"direction"`
}

type RouteID string
type TripID string

type TripDirection map[TripID]string

type RouteTrips map[RouteID]TripDirection

var routeDirections = RouteTrips{}

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

func getDirection(route_id RouteID, trip_id TripID) string {
	return routeDirections[route_id][trip_id]
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
				directionIcon := ">"
				if getDirection(routeID, tripID) != "0" {
					directionIcon = "<"
				}
				vehicles = append(vehicles, Vehicle{
					Latitude:  *entity.Vehicle.Position.Latitude,
					Longitude: *entity.Vehicle.Position.Longitude,
					RouteID:   *entity.Vehicle.Trip.RouteId,
					Direction: directionIcon,
				})
			}
		}
		allVehicles.Store(vehicles)
	}
}

func vehicleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(allVehicles.Load().([]Vehicle))
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
		rid := RouteID(row[0])
		tid := TripID(row[2])
		dir := row[5]
		if _, exists := routeDirections[rid]; !exists {
			routeDirections[rid] = TripDirection{}
		}

		routeDirections[rid][tid] = dir
	}
}

func main() {
	loadTripsData()

	go updateVehiclesPosition()

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicles", vehicleHandler)

	log.Println("Server running on port 8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))

	// if feed.Header.Timestamp != nil {
	// fmt.Printf("Feed last updated: %v\n", *feed.Header.Timestamp)
	// }
}
