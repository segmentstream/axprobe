package explore

import (
	"reflect"
	"testing"
)

func TestTokenDir(t *testing.T) {
	cases := map[string]string{
		"/root/.segmentstream/credentials/default-bigquery.json": "/root/.segmentstream",
		"/root/.config/gcloud/application_default_credentials.json": "/root/.config/gcloud",
		"/root/.config/gh/hosts.yml":                               "/root/.config/gh",
		"/root/.bash_history":                                      "", // shell noise
		"/root/.cache/pip/x":                                       "", // cache noise
		"/tmp/whatever":                                            "", // outside home
	}
	for in, want := range cases {
		if got := tokenDir(in); got != want {
			t.Errorf("tokenDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverTokenPaths(t *testing.T) {
	before := map[string]bool{
		"/root/.segmentstream/bin/segmentstream": true,
		"/root/.bashrc":                          true,
	}
	after := map[string]bool{
		"/root/.segmentstream/bin/segmentstream":                    true,
		"/root/.bashrc":                                             true,
		"/root/.segmentstream/credentials/default-bigquery.json":    true, // new token
		"/root/.config/gcloud/application_default_credentials.json": true, // new ADC
		"/root/.bash_history":                                       true, // new but noise
	}
	got := discoverTokenPaths(before, after)
	want := []string{"/root/.config/gcloud", "/root/.segmentstream"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverTokenPaths = %v, want %v", got, want)
	}
}
