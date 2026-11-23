package logtypes

import "testing"

func TestEventIDStableAndDistinct(t *testing.T) {
	src := SourceIdentity{LogSourceID: "src-1", SourceGeneration: "g1", ParserVersion: "p1"}
	a := EventID(src, RecordRange{Start: 10, End: 20})
	b := EventID(src, RecordRange{Start: 10, End: 20})
	c := EventID(src, RecordRange{Start: 10, End: 21})
	if a != b {
		t.Fatalf("same identity must yield same event_id")
	}
	if a == c {
		t.Fatalf("different record range must yield different event_id")
	}
	if len(a) != 64 {
		t.Fatalf("event_id should be sha256 hex, got len=%d", len(a))
	}
}

func TestBuildEventPopulatesIDs(t *testing.T) {
	src := SourceIdentity{LogSourceID: "s", SourceGeneration: "g", ParserVersion: "v"}
	ev := BuildEvent(src, RecordRange{Start: 1, End: 2}, "2026-09-20T00:00:00Z", "2026-09-20T00:00:01Z", "INFO", "stdout", "hello")
	if ev.EventID == "" || ev.CanonicalHash == "" {
		t.Fatal("event_id and canonical hash required")
	}
	if ev.EventID == ev.CanonicalHash {
		t.Fatal("event identity and content hash must differ")
	}
}

func TestStreamFieldAllowlist(t *testing.T) {
	// 契约 §6.5 白名单
	for _, ok := range []string{
		"worker_id", "instance_id", "log_source_id", "source_generation", "level", "stream",
	} {
		if !StreamAllowed(ok) {
			t.Fatalf("%s must be allowed in stream identity", ok)
		}
	}
	for _, bad := range []string{"event_id", "message", "_time", "source", "player_uuid"} {
		if StreamAllowed(bad) {
			t.Fatalf("%s must not enter stream identity", bad)
		}
	}
}
