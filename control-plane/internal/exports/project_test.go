package exports

import (
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

func TestHashesIn(t *testing.T) {
	a, b := "b3:"+strings.Repeat("a", 64), "b3:"+strings.Repeat("b", 64)
	got := hashesIn([]byte(`{"artifact":{"hash":"` + b + `"},"card":"` + a + `","list":["` + b + `","b3:short"],"fingerprint":"` +
		strings.Repeat("c", 64) + `"}`))
	if strings.Join(got, ",") != a+","+b {
		t.Fatalf("hashes %v", got)
	}
	if hashesIn([]byte("not json")) != nil {
		t.Error("a broken payload names hashes")
	}
}

func TestBundlePaths(t *testing.T) {
	h := "b3:" + strings.Repeat("ab", 32)
	if got := BlobPath(h); got != "cas/b3/ab/"+strings.Repeat("ab", 32) {
		t.Errorf("blob path %s", got)
	}
	if got := DatasetDir("dataset/fleurs-he", "2026-10-04.0123456789ab"); got != "datasets/dataset/fleurs-he/2026-10-04.0123456789ab" {
		t.Errorf("dataset dir %s", got)
	}
}

func TestKindRankPutsDependenciesFirst(t *testing.T) {
	order := []string{registry.KindStepKind, registry.KindNormalizer, registry.KindDataset, registry.KindGoldenSet, registry.KindModel}
	for i := 1; i < len(order); i++ {
		if kindRank(order[i-1]) >= kindRank(order[i]) {
			t.Errorf("%s ranks after %s", order[i-1], order[i])
		}
	}
}
