package telemetry

import (
	"testing"
	"time"
)

func TestThin(t *testing.T) {
	pts := make([]Point, 101)
	for i := range pts {
		s := int64(i)
		pts[i] = Point{Name: "loss", Step: &s, Value: float64(i), WallTime: time.Unix(int64(i), 0)}
	}
	tests := []struct {
		limit, want int
	}{{0, 101}, {200, 101}, {11, 11}, {2, 2}, {1, 1}}
	for _, tt := range tests {
		got := Thin(pts, tt.limit)
		if len(got) != tt.want {
			t.Fatalf("Thin(%d) = %d points, want %d", tt.limit, len(got), tt.want)
		}
		if got[len(got)-1].Value != 100 {
			t.Fatalf("Thin(%d) lost the last point", tt.limit)
		}
		if tt.limit > 1 && got[0].Value != 0 {
			t.Fatalf("Thin(%d) lost the first point", tt.limit)
		}
	}
	if got := Thin(pts, 11); got[5].Value != 50 {
		t.Fatalf("Thin spreads unevenly: %v", got[5].Value)
	}
}
