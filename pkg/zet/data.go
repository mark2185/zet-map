package zet

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
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

type Stop struct {
	ID        string  `json:"id"`
	Latitude  float32 `json:"lat"`
	Longitude float32 `json:"lon"`
	Headsign  string  `json:"headsign"`
}
type Stops []Stop

type RouteID string
type TripID string

var routesToTrips = map[RouteID]map[TripID]Trip{}

var lastUpdateTimestamp uint64 = 0
var allVehiclesPerRoutes atomic.Value
var cachedScheduledDataFilename = ""
var cacheMutex sync.RWMutex = sync.RWMutex{}

var routesToStops = map[RouteID]Stops{}
var allStops = Stops{}

// Populate the routesToStops global variable.
// trips      has route_id -> trip_id (already loaded)
// stop_times has trip_id  -> stop_id
// stops      has stop_id  -> lat, lon, type (already loaded)
func loadStopTimes(data [][]string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	clear(routesToStops)

	tripToStops := map[TripID]Stops{}
	for _, line := range data {
		trip_id := TripID(line[0])
		stop_id := line[3]

		stop_idx := slices.IndexFunc(allStops, func(s Stop) bool { return s.ID == stop_id })
		if stop_idx == -1 {
			continue
		}

		if _, exists := tripToStops[trip_id]; !exists {
			tripToStops[trip_id] = Stops{}
		}
		tripToStops[trip_id] = append(tripToStops[trip_id], allStops[stop_idx])
	}

	for route_id, trip_ids := range routesToTrips {
		// go over all trips for a route to find all the stops for that route
		for trip_id := range trip_ids {
			if stops, exists := tripToStops[trip_id]; exists {
				routesToStops[route_id] = stops
			}
		}
	}
}

func loadStops(data [][]string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	clear(allStops)
	for _, line := range data {
		location_type := line[8]
		const platform = "0"
		// platforms are "logical groups" of stations, it's not the actual place
		// one gets on/off the tram/bus
		if location_type != platform {
			continue
		}
		lat, _ := strconv.ParseFloat(line[4], 32)
		lon, _ := strconv.ParseFloat(line[5], 32)
		allStops = append(allStops, Stop{
			ID:        line[0],
			Latitude:  float32(lat),
			Longitude: float32(lon),
			Headsign:  line[1],
		})
	}
}

// Populate the global routesToTrips map.
func loadTrips(data [][]string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	clear(routesToTrips)
	for _, row := range data {
		routeID := RouteID(row[0])
		if _, exists := routesToTrips[routeID]; !exists {
			routesToTrips[routeID] = map[TripID]Trip{}
		}

		tripID := TripID(row[2])
		headsign := row[3]
		direction := row[5]
		routesToTrips[routeID][tripID] = Trip{Headsign: headsign, Direction: direction}
	}
}

// Return a list of all Vehicles.
func getAllVehicles(feed *gtfs.FeedMessage) ([]*gtfs.VehiclePosition, error) {
	vehicles := []*gtfs.VehiclePosition{}
	for _, entity := range feed.Entity {
		if entity.Vehicle != nil {
			vehicles = append(vehicles, entity.Vehicle)
		}
	}
	return vehicles, nil
}

// Check if the scheduled data cache needs updating.
func isScheduledDataCacheValid() bool {
	resp, err := http.Head(scheduledDataURL)
	if err != nil {
		log.Println("Could not fetch scheduled data: ", err)
		return false
	}
	defer resp.Body.Close() // should be noop if there is no body

	// should be in the form of:
	//     attachment; filename=zet-gtfs-scheduled-000-00369.zip
	contentDisposition := resp.Header.Get("Content-Disposition")
	isAttachment := strings.HasPrefix(contentDisposition, "attachment; filename=zet-gtfs-scheduled")
	matchesCachedValue := contentDisposition == cachedScheduledDataFilename
	return isAttachment && matchesCachedValue
}

// Fetch the latest known locations of all vehicles.
func fetchRealtimeData(url string) (*gtfs.FeedMessage, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Failed to GET GTFS Realtime feed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Unexpected response code: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to read realtime data response body: %v", err)
	}

	feed := &gtfs.FeedMessage{}
	if err := proto.Unmarshal(data, feed); err != nil {
		return nil, fmt.Errorf("Failed to parse protobuf: %v", err)
	}
	return feed, nil
}

func getStops(route_ID RouteID) ([]Stop, bool) {
	stops, exists := routesToStops[route_ID]
	if !exists {
		return nil, false
	}
	return stops, true
}

// Get a specific trip from a route, since each route has several possible trips.
func getTrip(route_id RouteID, trip_id TripID) (Trip, bool) {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	route, exists := routesToTrips[route_id]
	if exists {
		if trip, exists := route[trip_id]; exists {
			return trip, true
		}
	}
	return Trip{}, false
}

// Fetch zip file containing fixed schedules, stop locations, and additional data.
func fetchScheduledData(url string) (*zip.Reader, error) {
	resp, err := http.Get(url)
	if err != nil {
		log.Println("Could not fetch scheduled data: ", err)
		return nil, err
	}
	defer resp.Body.Close()

	// it is numbered, this way we can check if it has been updated
	cachedScheduledDataFilename = resp.Header.Get("Content-Disposition")

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("Could not read scheduled data response: ", err)
		return nil, err
	}

	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		log.Println("Could not create a zip reader for scheduled data: ", err)
		return nil, err
	}

	return reader, nil
}

// Group all vehicles on a given route.
func getVehiclesPerRoutes(vehicles []*gtfs.VehiclePosition) map[RouteID]Vehicles {
	routes := map[RouteID]Vehicles{}

	for _, v := range vehicles {
		routeID := RouteID(v.GetTrip().GetRouteId())
		if _, exists := routes[routeID]; !exists {
			routes[routeID] = Vehicles{}
		}

		tripID := TripID(v.GetTrip().GetTripId())
		trip, exists := getTrip(routeID, tripID)
		// the trip may not exist if the cache is out of date
		if !exists {
			if !isScheduledDataCacheValid() {
				log.Printf("Refetching trips data because either routeID %v or tripID %v don't exist in the cache\n", routeID, tripID)

				if err := updateScheduledDataCache(); err != nil {
					log.Println("Could not update scheduled data cache: ", err)
					continue
				}

				trip, exists = getTrip(routeID, tripID) // it should exist now
				if !exists {
					log.Printf("Route (route ID: %v, trip ID: %v) doesn't exist even after refetching data, this should not happen\n", routeID, tripID)
				}
			} else {
				// log.Printf("%v or %v don't exist in the cache, but the cache is up to date (%s)\n", routeID, tripID, cachedScheduledDataFilename)
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

func calculateVehicleBearings(oldRoutes, newRoutes map[RouteID]Vehicles) map[RouteID]Vehicles {
	for routeID, newRouteVehicles := range newRoutes {
		// new route was added, no reason to calculate anything, can't yet determine the direction
		if _, exists := oldRoutes[routeID]; !exists {
			continue
		}

		oldRouteVehicles := oldRoutes[routeID]
		for i, newVehicle := range newRouteVehicles {
			sameID := func(v Vehicle) bool { return newVehicle.ID == v.ID }

			oldVehicleIdx := slices.IndexFunc(oldRoutes[routeID], sameID)
			// a new vehicle, can't yet determine the direction
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
			moveThreshold := 1e-5 // a guesstimate
			distance := utils.CalculateDistance(oldPosition, newPosition)
			if distance < moveThreshold {
				continue
			}

			// if the bearing does not differ much, ignore the update
			threshold := float64(5) // degrees
			if math.Abs(float64(newAzimuth-oldVehicle.Direction)) > threshold {
				newRoutes[routeID][i].Direction = newAzimuth
			}
		}
	}
	return newRoutes
}

func FetchRealtimeDataLoop() {
	for {
		time.Sleep(2 * time.Second)

		feed, err := fetchRealtimeData(gtfsURL)
		if err != nil {
			log.Printf("Failed to fetch realtime GTFS data: %v\n", err)
			continue
		}

		if feed.Header.Timestamp != nil {
			headerTimestamp := *feed.Header.Timestamp
			cachedTimestamp := atomic.LoadUint64(&lastUpdateTimestamp)
			if headerTimestamp <= cachedTimestamp {
				// data is up to date
				continue
			}
			atomic.StoreUint64(&lastUpdateTimestamp, headerTimestamp)
		} else {
			log.Println("Realtime GTFS header does not have a timestamp, please investigate")
			continue
		}

		vehicles, err := getAllVehicles(feed)
		if err != nil {
			log.Printf("Failed to get vehicles data: %v", err)
			continue
		}

		newVehiclesPerRoutes := getVehiclesPerRoutes(vehicles)
		oldVehiclesPerRoutes := allVehiclesPerRoutes.Load().(map[RouteID]Vehicles)

		updatedVehiclesPerRoutes := calculateVehicleBearings(oldVehiclesPerRoutes, newVehiclesPerRoutes)

		allVehiclesPerRoutes.Store(updatedVehiclesPerRoutes)
	}
}

func readCsvFromZip(file *zip.File) ([][]string, error) {
	decompressedData, err := utils.ReadZipFile(file)
	if err != nil {
		log.Println("Couldn't unzip ", file)
		return nil, err
	}

	csvReader := csv.NewReader(bytes.NewReader(decompressedData))
	// read the header since we don't need it
	if _, err := csvReader.Read(); err != nil {
		log.Println("Could not read CSV header from ", file.Name)
		return nil, err
	}
	// read the rest
	csvData, err := csvReader.ReadAll()
	if err != nil {
		log.Println("Could not read CSV from ", file.Name)
		return nil, err
	}
	return csvData, nil
}

// Clear current global scheduled data cache and load up all the new data.
func updateScheduledDataCache() error {
	reader, err := fetchScheduledData(scheduledDataURL)
	if err != nil {
		log.Println("Could not fetch scheduled data: ", err)
		return err
	}

	// the scheduled data consists of:
	//     - agency.txt
	//     - calendar.txt
	//     - calendar_dates.txt
	//     - feed_info.txt
	//     - routes.txt
	//     - shapes.txt
	//     * stop_times.txt
	//     * stops.txt
	//     * trips.txt
	// we're only interested in the ones marked with *

	for _, zipFile := range reader.File {
		// TODO: make sure stops are be loaded before stop times
		switch zipFile.Name {
		case "stops.txt":
			data, err := readCsvFromZip(zipFile)
			if err != nil {
				return err
			}
			log.Println("Started parsing stops")
			loadStops(data)
			log.Println("Parsed stops")
		case "trips.txt":
			data, err := readCsvFromZip(zipFile)
			if err != nil {
				return err
			}
			log.Println("Started parsing trips")
			loadTrips(data)
			log.Println("Parsed trips")
		default:
			// ignore other files
		}
	}
	for _, zipFile := range reader.File {
		if zipFile.Name == "stop_times.txt" {
			data, err := readCsvFromZip(zipFile)
			if err != nil {
				return err
			}
			log.Println("Started parsing stop times")
			loadStopTimes(data)
			log.Println("Parsed stop times")
		}
	}
	return nil
}

// Load the scheduled data into memory and the initial realtime data,
// this includes stops, trams, and busses.
func FetchInitialData() error {
	if err := updateScheduledDataCache(); err != nil {
		log.Println("Could not update scheduled data cache: ", err)
		return err
	}

	feed, err := fetchRealtimeData(gtfsURL)
	if err != nil {
		log.Println("Could not fetch realtime data: ", err)
		return err
	}

	// cached so we don't do unnecessary work if the
	// realtime data cache is up to date later on
	if feed.Header.Timestamp != nil {
		atomic.StoreUint64(&lastUpdateTimestamp, *feed.Header.Timestamp)
	}

	vehicles, err := getAllVehicles(feed)
	if err != nil {
		log.Fatalf("Failed to load initial vehicles data: %v", err)
		return err
	}

	allVehiclesPerRoutes.Store(getVehiclesPerRoutes(vehicles))
	return nil
}
