package opencode

import (
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestScanSessionsMapsOnlyRequestedSessions(t *testing.T) {
	t.Parallel()

	dbPath, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	insertSession(t, rw, "aaa_sample", "", 1000, 1000, 0)
	insertAssistantMessage(t, rw, "msg_sample", "aaa_sample", 1100, 1200, 1, 1)
	insertPart(t, rw, "prt_sample", "msg_sample", "aaa_sample", 1150, 1150, textBody("sampled"))
	insertSession(t, rw, "zzz_bad", "", 2000, 2000, 0)
	if _, err := rw.Exec(`UPDATE session SET model = ? WHERE id = ?`, "{not json", "zzz_bad"); err != nil {
		t.Fatalf("make unsampled session model malformed: %v", err)
	}
	insertAssistantMessage(t, rw, "msg_bad", "zzz_bad", 2100, 2200, 1, 1)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw db: %v", err)
	}

	errs := &collectErrs{}
	out := make(chan canonical.Event, 64)
	err := ScanSessions(ctxBG(), dbPath, "opencode:test", []string{"aaa_sample"}, out, ScanSessionsOptions{
		Logger:  silentLogger(),
		OnError: errs.onError,
	})
	if err != nil {
		t.Fatalf("ScanSessions: %v", err)
	}
	if errs.count() != 0 {
		t.Fatalf("warnings = %d, want none from unsampled malformed session", errs.count())
	}

	var sessions []string
	for _, ev := range drainAll(out) {
		if started, ok := ev.(canonical.SessionStartedEvent); ok {
			sessions = append(sessions, started.NativeID)
		}
	}
	if strings.Join(sessions, ",") != "aaa_sample" {
		t.Fatalf("session started ids = %v, want only aaa_sample", sessions)
	}
}
