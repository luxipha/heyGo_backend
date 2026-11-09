package tripsafety

import (
	"math"
	"testing"
)

func TestRouteCorridorDistance(t *testing.T) {
	line := [][]float64{{3.9, 6.9}, {3.95, 6.95}}
	if distance := routeDistance(Point{6.925, 3.925}, line); distance > 1 {
		t.Fatalf("point on line: %.1f", distance)
	}
	if distance := routeDistance(Point{6.88, 3.98}, line); distance < 500 {
		t.Fatalf("point off corridor: %.1f", distance)
	}
	if !math.IsInf(routeDistance(Point{}, nil), 1) {
		t.Fatal("missing route invented a corridor")
	}
	if metres(Point{6.9, 3.9}, Point{6.9, 3.9001}) > 60 {
		t.Fatal("small GPS movement misclassified")
	}
}
