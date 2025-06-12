package zet

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/mark2185/zet/pkg/utils"
	"google.golang.org/protobuf/proto"
)

const gtfsURL = "https://zet.hr/gtfs-rt-protobuf"
const scheduledDataURL = "https://zet.hr/gtfs-scheduled/latest"

type Vehicle struct {
	ID        string  `json:"id"`
	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`
	Headsign  string  `json:"headsign"`
	Direction int     `json:"direction"`
}
type Vehicles []Vehicle

type Trip struct {
	Headsign  string
	Direction string
}

type RouteID string
type TripID string
type Trips map[TripID]Trip

type RoutesToTrips map[RouteID]Trips

var lastUpdateTimestamp uint64 = 0
var allVehicles atomic.Value
var routesToTrips = RoutesToTrips{}
var cachedTripsFilename = ""
var mu sync.RWMutex = sync.RWMutex{}

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

func FetchInitialData() error {
	tripsData, err := getTripsData()
	if err != nil {
		log.Println("Could not get trips data: ", err)
		return err
	}

	for _, row := range tripsData {
		routeID := RouteID(row[0])
		if _, exists := routesToTrips[routeID]; !exists {
			routesToTrips[routeID] = Trips{}
		}

		tripID := TripID(row[2])
		routesToTrips[routeID][tripID] = Trip{Headsign: row[3], Direction: row[5]}
	}

	feed, err := fetchGTFSRealTime(gtfsURL)
	if err != nil {
		return err
	}

	if feed.Header.Timestamp != nil {
		atomic.StoreUint64(&lastUpdateTimestamp, *feed.Header.Timestamp)
	}
	vehicles, err := getVehiclesData(feed)
	if err != nil {
		log.Fatalf("Failed to load initial data: %v", err)
		return err
	}

	allVehicles.Store(getRoutes(vehicles))
	return nil
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

func isTripsDataStale() bool {
	resp, err := http.Head(scheduledDataURL)
	if err != nil {
		log.Println("Could not check for trips data: ", err)
		return false
	}
	defer resp.Body.Close() // should be noop if there is no body

	// should be in the form of:
	//     attachment; filename=zet-gtfs-scheduled-000-00369.zip
	contentDisposition := resp.Header.Get("Content-Disposition")
	isAttachment := strings.HasPrefix(contentDisposition, "attachment; filename=zet-gtfs-scheduled")
	matchesCachedValue := contentDisposition == cachedTripsFilename
	return isAttachment && !matchesCachedValue
}

func getTrip(route_id RouteID, trip_id TripID) (Trip, bool) {
	mu.RLock()
	defer mu.RUnlock()
	route, exists := routesToTrips[route_id]
	if exists {
		if trip, exists := route[trip_id]; exists {
			return trip, true
		}
	}
	return Trip{}, false
}

func getTripsData() ([][]string, error) {
	tripsData, err := fetchTripsData()
	if err != nil {
		log.Println("Could not open trips info: ", err)
		return nil, err
	}

	tripsReader := csv.NewReader(bytes.NewReader(tripsData))

	_, err = tripsReader.Read() // read the header
	if err != nil {
		log.Printf("Could not parse CSV: %v\n", err)
	}

	return tripsReader.ReadAll() // read the rest
}

func fetchTripsData() ([]byte, error) {
	resp, err := http.Get(scheduledDataURL)
	if err != nil {
		log.Println("Could not fetch trips data: ", err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("Could not read trips data response: ", err)
		return nil, err
	}

	zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		log.Println("Could not create a zip reader for trips data: ", err)
		return nil, err
	}

	for _, zipFile := range zipReader.File {
		if zipFile.Name == "trips.txt" {
			unzippedFileBytes, err := utils.ReadZipFile(zipFile)
			if err != nil {
				log.Println("Could not unzip trips.txt: ", err)
				return nil, err
			}
			cachedTripsFilename = resp.Header.Get("Content-Disposition")
			return unzippedFileBytes, nil
		}
	}
	return nil, errors.New("trips.txt not present in response")
}

func getRoutes(vehicles []*gtfs.VehiclePosition) map[RouteID]Vehicles {
	routes := map[RouteID]Vehicles{}
	for _, v := range vehicles {
		routeID := RouteID(v.GetTrip().GetRouteId())
		tripID := TripID(v.GetTrip().GetTripId())
		if _, exists := routes[routeID]; !exists {
			routes[routeID] = Vehicles{}
		}
		trip, exists := getTrip(routeID, tripID)
		if !exists {
			if isTripsDataStale() { // cache needs updating
				log.Printf("Refetching trips data because routeID %v or tripID %v don't exist", routeID, tripID)
				tripsData, err := getTripsData()
				if err != nil {
					mu.Lock()
					defer mu.Unlock()
					clear(routesToTrips)
					for _, row := range tripsData {
						routeID := RouteID(row[0])
						if _, exists := routesToTrips[routeID]; !exists {
							routesToTrips[routeID] = Trips{}
						}

						tripID := TripID(row[2])
						routesToTrips[routeID][tripID] = Trip{Headsign: row[3], Direction: row[5]}
					}
				}
				trip, exists = getTrip(routeID, tripID) // it should exist now
				if !exists {
					log.Printf("Route (route ID: %v, trip ID: %v) doesn't exist even after refetching data, this should not happen\n", routeID, tripID)
				}
			} else {
				log.Printf("%v or %v don't exist in the cache, but the cache is up to date (%s)\n", routeID, tripID, cachedTripsFilename)
			}
		}
		routes[routeID] = append(routes[routeID], Vehicle{
			ID:        v.GetVehicle().GetId(),
			Latitude:  v.GetPosition().GetLatitude(),
			Longitude: v.GetPosition().GetLongitude(),
			Headsign:  trip.Headsign,
		})
	}
	return routes
}

func FetchDataLoop() {
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

			oldPosition := utils.Point{Lat: float64(oldVehicle.Latitude), Lon: float64(oldVehicle.Longitude)}
			newPosition := utils.Point{Lat: float64(newVehicle.Latitude), Lon: float64(newVehicle.Longitude)}

			newAzimuth := int(utils.CalculateBearing(oldPosition, newPosition))
			oldAzimuth := oldVehicle.Direction

			newRoutes[routeID][i].Direction = oldAzimuth

			// if the position hasn't changed a lot, it's probably a sitting duck
			moveThreshold := 1e-5
			distance := utils.CalculateDistance(oldPosition, newPosition)
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
