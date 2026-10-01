package notify

import (
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

func TestWriteQueue(t *testing.T) {
	half := 0.5
	tests := []struct {
		name  string
		items []QueueItem
		want  []string
	}{
		{name: "empty", want: []string{"Queue: empty"}},
		{
			name: "running and waiting",
			items: []QueueItem{
				{Kind: "nemotron_train@1", JobKind: "training", State: "running", Where: "rtx6000 · card 0", Progress: &half, Message: "step 500/1000"},
				{Kind: "nemotron_transcribe@1", JobKind: "eval", State: "waiting"},
			},
			want: []string{"Queue: 1 running, 1 waiting", "• nemotron_train@1 (training): 50% · rtx6000 · card 0 · step 500/1000",
				"• nemotron_transcribe@1 (eval): waiting"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("Queue:")
			writeQueue(&b, tt.items)
			for _, w := range tt.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("queue text %q lacks %q", b.String(), w)
				}
			}
		})
	}
}

func TestWriteQueueCapsTheList(t *testing.T) {
	items := make([]QueueItem, maxListed+3)
	for i := range items {
		items[i] = QueueItem{Kind: "echo@1", JobKind: "cpu", State: "waiting"}
	}
	var b strings.Builder
	writeQueue(&b, items)
	if got := strings.Count(b.String(), "\n• echo@1"); got != maxListed {
		t.Errorf("listed %d entries, want %d", got, maxListed)
	}
	if !strings.Contains(b.String(), "… 3 more") {
		t.Errorf("no remainder line in %q", b.String())
	}
}

func TestClassifyLowSpace(t *testing.T) {
	r := events.Record{Topic: "storage", Type: "storage.low_space",
		Payload: []byte(`{"totalBytes":492000000000,"freeBytes":36000000000,"lowFreeFraction":0.15,"evictableArtifacts":12,"evictableBytes":88000000000,"permanent":true}`)}
	n, ok := Classify(r)
	if !ok || n.Class != ClassFailure || n.Title != "Content store low on space" {
		t.Fatalf("notice %+v, %v", n, ok)
	}
	for _, want := range []string{"36 GB free of 492 GB (7 %)", "12 superseded training states (88 GB)", "permanent"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body %q lacks %q", n.Body, want)
		}
	}
}
