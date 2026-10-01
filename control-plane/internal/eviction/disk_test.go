package eviction

import "testing"

func TestDiskLow(t *testing.T) {
	tests := []struct {
		name string
		d    Disk
		want bool
	}{
		{"plenty", Disk{TotalBytes: 100, FreeBytes: 50, LowFreeFraction: 0.15}, false},
		{"just above", Disk{TotalBytes: 100, FreeBytes: 15, LowFreeFraction: 0.15}, false},
		{"low", Disk{TotalBytes: 100, FreeBytes: 7, LowFreeFraction: 0.15}, true},
		{"unknown size", Disk{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.Low(); got != tt.want {
				t.Errorf("Low() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiskOfReadsTheFilesystem(t *testing.T) {
	d, err := DiskOf(t.TempDir(), 0.15)
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalBytes <= 0 || d.FreeBytes < 0 || d.FreeBytes > d.TotalBytes || d.LowFreeFraction != 0.15 {
		t.Fatalf("disk %+v", d)
	}
}
