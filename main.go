package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
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
	Direction int     `json:"direction"`
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

type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func calculateBearing(p1, p2 Point) float64 {
	dx := p2.Lon - p1.Lon
	dy := p2.Lat - p1.Lat

	angle := math.Atan2(dy, dx) * 180 / math.Pi
	if angle < 0 {
		return angle + 360
	}
	return angle
}

func calculateDistance(p1, p2 Point) float64 {
	dx := p2.Lon - p1.Lon
	dy := p2.Lat - p1.Lat

	return math.Sqrt(dx*dx + dy*dy)
}

func getVehiclesData(feed *gtfs.FeedMessage) ([]*gtfs.VehiclePosition, error) {
	vehicles := []*gtfs.VehiclePosition{}
	for _, entity := range feed.Entity {
		if entity.Vehicle != nil {
			vehicles = append(vehicles, entity.Vehicle)
		}
	}
	return vehicles, nil
}

func getRoutes(vehicles []*gtfs.VehiclePosition) map[RouteID]Vehicles {
	routes := map[RouteID]Vehicles{}
	for _, v := range vehicles {
		routeID := RouteID(v.GetTrip().GetRouteId())
		if _, exists := routes[routeID]; !exists {
			routes[routeID] = Vehicles{}
		}
		routes[routeID] = append(routes[routeID], Vehicle{
			ID:        v.GetVehicle().GetId(),
			Latitude:  v.GetPosition().GetLatitude(),
			Longitude: v.GetPosition().GetLongitude(),
			Headsign:  getTrip(routeID, TripID(v.GetTrip().GetTripId())).Headsign,
		})
	}
	return routes
}

func calculateVehicleBearings(oldRoutes, newRoutes map[RouteID]Vehicles) map[RouteID]Vehicles {
	for routeID, vehicles := range newRoutes {
		// new route was added, no reason to calculate anything, all the directions are irrelevant
		if _, exists := oldRoutes[routeID]; !exists {
			continue
		}

		oldRouteVehicles := oldRoutes[routeID]
		for i, newVehicle := range vehicles {
			sameID := func(v Vehicle) bool { return newVehicle.ID == v.ID }

			oldVehicleIdx := slices.IndexFunc(oldRoutes[routeID], sameID)
			// a new vehicle, direction is irrelevant
			if oldVehicleIdx == -1 {
				continue
			}

			oldVehicle := oldRouteVehicles[oldVehicleIdx]

			oldPosition := Point{Lat: float64(oldVehicle.Latitude), Lon: float64(oldVehicle.Longitude)}
			newPosition := Point{Lat: float64(newVehicle.Latitude), Lon: float64(newVehicle.Longitude)}

			newAzimuth := int(calculateBearing(oldPosition, newPosition))
			oldAzimuth := oldVehicle.Direction

			newRoutes[routeID][i].Direction = oldAzimuth

			// if the position hasn't changed a lot, it's probably a sitting duck
			moveThreshold := 1e-5
			distance := calculateDistance(oldPosition, newPosition)
			if distance < moveThreshold {
				continue
			}

			// if the bearing does not differ much, ignore the update
			threshold := float64(3) // degrees
			if math.Abs(float64(newAzimuth-oldVehicle.Direction)) > threshold {
				newRoutes[routeID][i].Direction = newAzimuth
			}

		}
	}

	return newRoutes
}
func faviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "data/favicon-32x32.png")
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

	allVehicles.Store(getRoutes(vehicles))

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

			newRoutes := getRoutes(vehicles)
			oldRoutes := allVehicles.Load().(map[RouteID]Vehicles)

			updatedRoutes := calculateVehicleBearings(oldRoutes, newRoutes)

			allVehicles.Store(updatedRoutes)
		}
	}()

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/favicon.ico", faviconHandler)
	// http.HandleFunc("/vehicles", vehicleHandler)
	http.HandleFunc("/events", sseHandler)

	log.Println("Server running on port 8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
