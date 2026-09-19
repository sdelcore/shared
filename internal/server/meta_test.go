package server

import (
	"testing"
)

func TestCreationTimeSurvivesDeployHistoryRetention(t *testing.T) {
	m, err := newMetaStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	m.record("site", "alice", "first")
	_, _, createdAt, _ := m.stats("site")
	if createdAt == "" {
		t.Fatal("creation time was not recorded")
	}

	for range maxDeployHistory {
		m.record("site", "alice", "update")
	}
	_, _, got, deploys := m.stats("site")
	if got != createdAt {
		t.Fatalf("creation time changed after history trimming: got %q, want %q", got, createdAt)
	}
	if deploys != maxDeployHistory+1 {
		t.Fatalf("deploy count = %d, want %d", deploys, maxDeployHistory+1)
	}
}
