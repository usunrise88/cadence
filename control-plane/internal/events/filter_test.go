package events

import "testing"

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern, topic string
		want           bool
	}{
		{"*", "anything.at.all", true},
		{"run.123.*", "run.123.metrics", true},
		{"run.123.*", "run.123.metrics.loss", true},
		{"run.123.*", "run.123", false},
		{"run.123.*", "run.1234.metrics", false},
		{"entity.project.*", "entity.project.prj_1", true},
		{"entity.project.*", "entity.project_x.1", false},
		{"entity.project.prj_1", "entity.project.prj_1", true},
		{"entity.project.prj_1", "entity.project.prj_12", false},
		{"queue", "queue", true},
		{"queue", "queue.x", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"~"+tt.topic, func(t *testing.T) {
			p, err := ParsePattern(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Match(tt.topic); got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
			}
		})
	}
}

func TestParsePatternRejects(t *testing.T) {
	for _, s := range []string{"", "run.*.metrics", "run.12*", "run..x", "*.x", "run.123.*x"} {
		if _, err := ParsePattern(s); err == nil {
			t.Errorf("ParsePattern(%q) succeeded, want error", s)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	f, err := ParseFilter("entity.project.*, agent.session.*", "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		rec  Record
		want bool
	}{
		{"own project", Record{Topic: "entity.project.prj_a", ProjectID: "prj_a"}, true},
		{"other project", Record{Topic: "entity.project.prj_b", ProjectID: "prj_b"}, false},
		{"registry event without project", Record{Topic: "agent.session.9"}, true},
		{"topic not subscribed", Record{Topic: "queue", ProjectID: "prj_a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Match(tt.rec); got != tt.want {
				t.Errorf("Match = %v, want %v", got, tt.want)
			}
		})
	}
	all, err := ParseFilter("x.y,*", "")
	if err != nil || len(all.Patterns) != 0 {
		t.Errorf("a lone * should match everything, got %+v, %v", all, err)
	}
}

func TestFilterSQL(t *testing.T) {
	f, err := ParseFilter("pipeline_run.*,queue", "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	where, args := f.sql(7)
	want := "seq > $1 AND (project_id IS NULL OR project_id = $2) AND (topic = ANY($3) OR topic LIKE ANY($4))"
	if where != want {
		t.Errorf("where = %q\nwant    %q", where, want)
	}
	if like := args[3].([]string)[0]; like != `pipeline\_run._%` {
		t.Errorf("like pattern = %q", like)
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	h := NewHub(1)
	fast, slow := h.Subscribe(), h.Subscribe()
	h.Publish(Record{Seq: 1})
	<-fast.C
	h.Publish(Record{Seq: 2}) // slow's buffer is still full: it is dropped
	if r := <-fast.C; r.Seq != 2 {
		t.Fatalf("fast got %d", r.Seq)
	}
	<-slow.C // the buffered event
	if _, ok := <-slow.C; ok {
		t.Fatal("slow subscriber should be closed")
	}
	h.Close()
	if _, ok := <-fast.C; ok {
		t.Fatal("Close should end subscriptions")
	}
	if _, ok := <-h.Subscribe().C; ok {
		t.Fatal("subscribing to a closed hub should give a closed channel")
	}
	fast.Close() // idempotent after the hub closed it
}
