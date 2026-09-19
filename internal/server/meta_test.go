package server

import "testing"

func TestCreationTimeSurvivesDeployHistoryRetention(t *testing.T) {
	dir := t.TempDir()
	m, err := newMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	const createdAt = "2001-02-03T04:05:06Z"
	m.mu.Lock()
	m.cache["site"] = &siteMeta{
		CreatedAt: createdAt,
		Deploys: []deployRecord{{
			Seq:  1,
			Time: createdAt,
		}},
	}
	m.persist("site")
	m.mu.Unlock()

	for range maxDeployHistory {
		m.record("site", "alice", "update")
	}

	reopened, err := newMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, got, deploys := reopened.stats("site")
	if got != createdAt {
		t.Fatalf("creation time changed after history trimming: got %q, want %q", got, createdAt)
	}
	if deploys != maxDeployHistory+1 {
		t.Fatalf("deploy count = %d, want %d", deploys, maxDeployHistory+1)
	}
}
