package collector

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
)

var update = flag.Bool("update", false, "rewrite the golden exposition files in testdata/")

// static is a Snapshotter returning a fixed snapshot.
type static[T any] struct{ snap *T }

func (s static[T]) Snapshot() *T { return s.snap }

// checkGolden compares the exposition of c with testdata/<name>.prom, and
// lints it. Run the tests with -update to regenerate the golden file after an
// intended change, then review the diff.
func checkGolden(t *testing.T, c prometheus.Collector, name string) {
	t.Helper()
	path := filepath.Join("testdata", name+".prom")

	if *update {
		reg := prometheus.NewPedanticRegistry()
		reg.MustRegister(c)
		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("gathering: %v", err)
		}
		var buf bytes.Buffer
		enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
		for _, mf := range families {
			if err := enc.Encode(mf); err != nil {
				t.Fatalf("encoding: %v", err)
			}
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
	}

	golden, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening golden file (run with -update to create it): %v", err)
	}
	defer golden.Close()
	if cmpErr := testutil.CollectAndCompare(c, golden); cmpErr != nil {
		t.Errorf("exposition differs from %s (run with -update to accept):\n%v", path, cmpErr)
	}

	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatalf("linting: %v", err)
	}
	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}
}
