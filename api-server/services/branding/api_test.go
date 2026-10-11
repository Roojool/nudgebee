package branding

import "testing"

// A row can arrive by hand — psql, a restore, a migration — and bypass the
// normalization Upsert applies. `localhost:3000` vs `localhost` is the case
// that bites: the row looks correct in the table, the lookup strips the port,
// and the override silently does nothing with no error anywhere. The serving
// map is therefore keyed by the normalized host regardless of how the row was
// written.
func TestNormalizeHostStripsPortForServingKey(t *testing.T) {
	cases := map[string]string{
		"localhost:3000":              "localhost",
		"LOCALHOST:3000":              "localhost",
		"demo.partner.example.com:80": "demo.partner.example.com",
		"demo.partner.example.com.":   "demo.partner.example.com",
		"localhost":                   "localhost",
	}
	for stored, want := range cases {
		if got := NormalizeHost(stored); got != want {
			t.Errorf("a row stored as %q must serve under %q, got %q", stored, want, got)
		}
	}
}
