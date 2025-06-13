package utils

import (
	"archive/zip"
	"io"
	"math"
)

type Point struct {
	Lat float64 // float64 because of functions in the math package
	Lon float64 // float64 because of functions in the math package
}

func CalculateBearing(p1, p2 Point) float64 {
	dx := p2.Lon - p1.Lon
	dy := p2.Lat - p1.Lat

	angle := math.Atan2(dy, dx) * 180 / math.Pi
	if angle < 0 {
		return angle + 360
	}
	return angle
}

func CalculateDistance(p1, p2 Point) float64 {
	dx := p2.Lon - p1.Lon
	dy := p2.Lat - p1.Lat

	return math.Sqrt(dx*dx + dy*dy)
}

func ReadZipFile(zf *zip.File) ([]byte, error) {
	f, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
