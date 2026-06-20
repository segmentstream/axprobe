package explore

import (
	"reflect"
	"testing"
)

func TestIsCacheableNewFile(t *testing.T) {
	cases := map[string]bool{
		"/root/.segmentstream/credentials/default-bigquery.json":    true,
		"/root/.config/gcloud/application_default_credentials.json": true,
		"/root/.config/gh/hosts.yml":                                true,
		"/root/.bash_history":                                       false, // shell noise
		"/root/.cache/pip/x":                                        false, // cache noise
		"/tmp/whatever":                                             false, // outside home
	}
	for in, want := range cases {
		if got := isCacheableNewFile(in); got != want {
			t.Errorf("isCacheableNewFile(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDiscoverTokenPaths(t *testing.T) {
	before := map[string]bool{
		"/root/.segmentstream/bin/segmentstream": true, // installed during setup, not new
		"/root/.bashrc":                          true,
	}
	after := map[string]bool{
		"/root/.segmentstream/bin/segmentstream":                    true, // unchanged → excluded
		"/root/.bashrc":                                             true,
		"/root/.segmentstream/credentials/default-bigquery.json":    true, // new token → kept
		"/root/.config/gcloud/application_default_credentials.json": true, // new ADC → kept
		"/root/.bash_history":                                       true, // new but noise → excluded
	}
	got := discoverTokenPaths(before, after)
	// Exact files (never the binary), sorted.
	want := []string{
		"/root/.config/gcloud/application_default_credentials.json",
		"/root/.segmentstream/credentials/default-bigquery.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverTokenPaths = %v, want %v", got, want)
	}
}
