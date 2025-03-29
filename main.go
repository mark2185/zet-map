package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

const gtfsURL = "https://zet.hr/gtfs-rt-protobuf"

type Vehicle struct {
	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`
	RouteID   string  `json:"route_id"`
}

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

type Filter struct {
	routeId string
}

func getAllVehicles(feed *gtfs.FeedMessage) []Vehicle {
	var vehicles []Vehicle
	for _, entity := range feed.Entity {
		if entity.Vehicle != nil {
			vehicles = append(vehicles, Vehicle{
				Latitude:  *entity.Vehicle.Position.Latitude,
				Longitude: *entity.Vehicle.Position.Longitude,
				RouteID:   *entity.Vehicle.Trip.RouteId,
			})
		}
	}

	return vehicles
}

func readGTFSRealtime(feed *gtfs.FeedMessage) {
	for _, entity := range feed.Entity {
		if entity.TripUpdate != nil {
			// fmt.Println("Trip Update: ", entity.TripUpdate)
		}
		if entity.Vehicle != nil {
			vehicle := entity.GetVehicle()
			routeId := vehicle.GetTrip().GetRouteId()
			if routeId == "5" {
				fmt.Println("Vehicle Position: ", vehicle)
			}
		}
		if entity.Alert != nil {
			// fmt.Println("Service Alert: ", entity.Alert)
		}
	}
}

const osmStaticMapURL = "https://static-maps.yandex.ru/1.x/?l=map&size=600,400&z=13"

func generateMap(lat, lon float64) {
	url := fmt.Sprintf("%s&pt=%f,%f,pm2rdl", osmStaticMapURL, lon, lat)

	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("Error fetching map: %v", err)
	}
	defer resp.Body.Close()

	file, err := os.Create("/tmp/map.png")
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer file.Close()

	_, err = file.ReadFrom(resp.Body)
	if err != nil {
		log.Fatalf("Error saving map image: %v", err)
	}

	fmt.Println("Map saved as /tmp/map.png")
}

func vehicleHandler(w http.ResponseWriter, r *http.Request) {
	feed, err := fetchGTFSRealTime(gtfsURL)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	vehicles := getAllVehicles(feed)

	w.Header().Set("Content-Type", "application/json")

	json.NewEncoder(w).Encode(vehicles)
}

func mapHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func main() {
	// feed, err := fetchGTFSRealTime(gtfsURL)
	// if err != nil {
	// log.Fatalf("Error fetching data: %v", err)
	// }

	// vehicles := getAllVehicles(feed)

	// log.Println(vehicles)

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicles", vehicleHandler)

	fmt.Println("Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))

	// if feed.Header.Timestamp != nil {
	// fmt.Printf("Feed last updated: %v\n", *feed.Header.Timestamp)
	// }

	// vehicles := getAllVehicles(feed)
	// readGTFSRealtime(feed)

	// generateMap(45.8, 15.9)
}
