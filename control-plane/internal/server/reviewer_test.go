package server

import (
	"net/http"
	"testing"
)

// A reviewer's session reaches its batch, the guidelines that batch pinned and nothing else of the repository.
func TestReviewerAllowed(t *testing.T) {
	const batch = "anb_1"
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, APIPrefix + "/batches/anb_1", true},
		{http.MethodGet, APIPrefix + "/batches/anb_1/guidelines", true},
		{http.MethodPost, APIPrefix + "/batches/anb_1/guidelines", false},
		{http.MethodGet, APIPrefix + "/batches/anb_2/guidelines", false},
		{http.MethodGet, APIPrefix + "/projects/calls/recipes/annotation/guidelines/default.md", false},
		{http.MethodGet, APIPrefix + "/registry/texts/b3:" + "00", false},
		{http.MethodGet, APIPrefix + "/batches/anb_1/batch-items", true},
	}
	for _, tt := range tests {
		if got := reviewerAllowed(tt.method, tt.path, batch); got != tt.want {
			t.Errorf("reviewerAllowed(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}
