package lint

import (
	"strings"
	"testing"
)

func TestGoalLeakage(t *testing.T) {
	// The exact leak from the first synthesized fixture.
	leaky := "Connect BigQuery: configure the warehouse and verify with the warehouse test — reach ready:true, using --port 8085 over the OAuth loopback."
	vocab := []string{"segmentstream warehouse configure --json", "segmentstream warehouse test", "segmentstream warehouse browse"}
	w := Goal(leaky, vocab)
	joined := strings.Join(w, "\n")

	for _, want := range []string{"ready:true", "--port", "loopback", "warehouse"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a warning mentioning %q; got:\n%s", want, joined)
		}
	}
}

func TestGoalClean(t *testing.T) {
	clean := "Connect my BigQuery to segmentstream using my Google account, into project acme-prod, a dataset called analytics in the US region. Make sure the connection actually works."
	if w := Goal(clean, []string{"segmentstream warehouse configure", "segmentstream warehouse test"}); len(w) != 0 {
		t.Errorf("clean user-level goal should not warn; got:\n%s", strings.Join(w, "\n"))
	}
}

func TestGoalDoesNotFlagFlagValuesOrBinaryPath(t *testing.T) {
	// Reproduces the false-positive from the live segmentstream.com run: the user's
	// own project/dataset values and the binary name must NOT be flagged.
	goal := "Connect segmentstream.com's analytics to BigQuery using my Google account, " +
		"into project segmentstream-dev, a dataset called segmentstream_com in US, and make sure it works."
	vocab := []string{
		"/root/.segmentstream/bin/segmentstream warehouse configure --project segmentstream-dev --dataset segmentstream_com --location US --create-dataset",
		"/root/.segmentstream/bin/segmentstream warehouse test",
	}
	w := Goal(goal, vocab)
	joined := strings.Join(w, "\n")
	for _, bad := range []string{"segmentstream", "project", "dataset", "segmentstream-dev", "segmentstream_com"} {
		if strings.Contains(joined, "\""+bad+"\"") {
			t.Errorf("should not flag %q (user value / binary); got:\n%s", bad, joined)
		}
	}
}
