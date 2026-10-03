package transcriptions

import (
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

func TestMsgType(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"type":"start","input":{"kind":"microphone"}}`, "start"},
		{`{"type": "summary", "audioS": 3}`, "summary"},        // Python's json.dumps puts a space after the colon
		{`{ "type" : "ping" }`, "ping"},                        // tolerant spacing
		{`{"seq":1,"target":"A","type":"partial"}`, "partial"}, // type within the head
		{`{"audioS":1,"rtf":0.1,"targets":{"A":{"profile":"160ms","steps":12,"stepMsP50":13.2}},"type":"summary"}`, "summary"},
		{`not json`, ""},
		{`{"type":5}`, ""},
	}
	for _, tt := range tests {
		if got := msgType([]byte(tt.in)); got != tt.want {
			t.Errorf("msgType(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	s := &Service{AllowedOrigins: []string{"https://cadence.example.org/"}}
	tests := []struct {
		name, host, origin, forwarded string
		want                          bool
	}{
		{"same host", "cadence.local:8080", "http://cadence.local:8080", "", true},
		{"same host https", "stand.example", "https://stand.example", "", true},
		{"no origin", "cadence.local:8080", "", "", false},
		{"foreign origin", "cadence.local:8080", "https://evil.example", "", false},
		{"other port", "cadence.local:8080", "http://cadence.local:9090", "", false},
		{"behind a proxy that rewrites Host", "control-plane:8080", "https://stand.example", "stand.example", true},
		{"allow-listed", "control-plane:8080", "https://cadence.example.org", "", true},
		{"not a web origin", "cadence.local", "file://cadence.local", "", false},
		{"opaque origin", "cadence.local", "null", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/transcriptions/x/stream", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.forwarded != "" {
				r.Header.Set("X-Forwarded-Host", tt.forwarded)
			}
			if got := s.OriginAllowed(r); got != tt.want {
				t.Fatalf("OriginAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTokens(t *testing.T) {
	a, ha := newToken()
	b, hb := newToken()
	if a == b || ha == hb || len(a) < 40 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if hashToken(a) != ha || hashToken(b) == ha {
		t.Fatal("hash mismatch")
	}
}

func TestShuffledIsAPermutation(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := shuffled(3)
		s := slices.Clone(p)
		slices.Sort(s)
		if !slices.Equal(s, []int{0, 1, 2}) {
			t.Fatalf("not a permutation: %v", p)
		}
		seen[string(rune('0'+p[0]))+string(rune('0'+p[1]))+string(rune('0'+p[2]))] = true
	}
	if len(seen) < 4 { // six orders exist; 200 draws all but surely see most of them
		t.Fatalf("blind orders look fixed: %v", seen)
	}
}

func TestChooseProfile(t *testing.T) {
	d := defaults.Get()
	ps := []profile{{Name: "offline"}, {Name: "80ms", LatencyMs: 80, ChunkMs: 80}, {Name: d.Eval.PrimaryProfile.Value, ChunkMs: 160}}
	if p, ok := chooseProfile(d, ps, ""); !ok || p.Name != d.Eval.PrimaryProfile.Value {
		t.Fatalf("default = %+v", p)
	}
	if p, ok := chooseProfile(d, ps, "80ms"); !ok || p.Name != "80ms" {
		t.Fatalf("requested = %+v", p)
	}
	if _, ok := chooseProfile(d, ps, "320ms"); ok {
		t.Fatal("an unknown profile was accepted")
	}
	if p, ok := chooseProfile(d, ps[:2], ""); !ok || p.Name != "80ms" {
		t.Fatalf("first streaming profile = %+v", p)
	}
}

func TestKnowsLanguage(t *testing.T) {
	tags := []string{"locale:he-IL", "locale:ru-RU", "family:x"}
	for _, tt := range []struct {
		tags []string
		lang string
		want bool
	}{
		{tags, "he-IL", true}, {tags, "he", true}, {tags, "ru", true}, {tags, "th-TH", false}, {tags, "auto", true}, {nil, "th-TH", true},
	} {
		if got, _ := knowsLanguage(tt.tags, tt.lang); got != tt.want {
			t.Errorf("knowsLanguage(%v, %s) = %v", tt.tags, tt.lang, got)
		}
	}
}

func TestHubPairsAWorkerThatDialledFirst(t *testing.T) {
	var h hub
	w1 := &workerConn{done: make(chan struct{})}
	if err := h.attachWorker("job_1", w1); err != nil {
		t.Fatal(err)
	}
	ls := &liveSession{id: "trs_1", jobID: "job_1", worker: make(chan *workerConn, 1), ended: make(chan steps.Outcome, 1)}
	if err := h.register(ls); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ls.worker:
		if got != w1 {
			t.Fatal("another socket was handed over")
		}
	default:
		t.Fatal("the parked socket was not handed over")
	}
	if h.unpark("job_1", w1) {
		t.Fatal("a handed-over socket was still parked")
	}
	// A dial while the session waits goes straight to it; a second one while the first waits is refused.
	w2, w3 := &workerConn{done: make(chan struct{})}, &workerConn{done: make(chan struct{})}
	if err := h.attachWorker("job_1", w2); err != nil {
		t.Fatal(err)
	}
	if err := h.attachWorker("job_1", w3); err == nil {
		t.Fatal("a second socket was accepted")
	}
	if !h.has("trs_1") {
		t.Fatal("the session is not registered")
	}
	if err := h.register(ls); err == nil {
		t.Fatal("a second socket for one session was registered")
	}
	h.jobEndedByJob("job_1", steps.Outcome{State: steps.StateDone})
	select {
	case o := <-ls.ended:
		if o.State != steps.StateDone {
			t.Fatalf("outcome %+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("no end notice")
	}
	h.unregister(ls)
	if h.has("trs_1") {
		t.Fatal("still registered")
	}
	// A worker parked twice for one job keeps the newer socket; the older one is finished.
	w4, w5 := &workerConn{done: make(chan struct{})}, &workerConn{done: make(chan struct{})}
	_ = h.attachWorker("job_2", w4)
	_ = h.attachWorker("job_2", w5)
	select {
	case <-w4.done:
	default:
		t.Fatal("the replaced socket was not finished")
	}
	h.stop()
	select {
	case <-w5.done:
	default:
		t.Fatal("stop left a parked socket")
	}
	if err := h.register(&liveSession{id: "trs_3", worker: make(chan *workerConn, 1)}); err == nil {
		t.Fatal("a stopped hub took a session")
	}
}

func TestHist(t *testing.T) {
	var h hist
	for i := 1; i <= 100; i++ {
		h.add(time.Duration(i) * time.Microsecond)
	}
	p50, p95, n := h.stats()
	if n != 100 || p50 != 50 || p95 != 95 {
		t.Fatalf("stats = %v %v %d", p50, p95, n)
	}
}
