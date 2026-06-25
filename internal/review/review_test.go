package review

import (
	"strings"
	"testing"
)

func TestReviewPostMortemExcerptDropsSpeculativeSections(t *testing.T) {
	pm := `What worked well
The state machine guided the agent through setup.

**Ideal command sequence**
$ tool run --json
# PROPOSED: add --health-host
$ tool run --json --health-host 127.0.0.1

**Where it stopped**
The command failed during readiness polling.`

	got := reviewPostMortemExcerpt(pm)
	if strings.Contains(got, "Ideal command sequence") {
		t.Fatalf("expected ideal section to be removed, got:\n%s", got)
	}
	if strings.Contains(got, "PROPOSED") || strings.Contains(got, "--health-host") {
		t.Fatalf("expected speculative proposed lines to be removed, got:\n%s", got)
	}
	if !strings.Contains(got, "The state machine guided") {
		t.Fatalf("expected factual context to be preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "The command failed during readiness polling") {
		t.Fatalf("expected later factual section to be preserved, got:\n%s", got)
	}
}
