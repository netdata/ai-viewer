package presenter

import "testing"

func TestTimelineBoundsObserve_TracksNullEndAndPointEvents(t *testing.T) {
	t.Parallel()

	var bounds timelineBounds
	bounds.observe(timelineSpan{ID: "late-running", StartTS: 30})
	bounds.observe(timelineSpan{ID: "early-point", StartTS: 10, EndTS: timelineInt64Ptr(10)})
	bounds.observe(timelineSpan{ID: "middle-ended", StartTS: 20, EndTS: timelineInt64Ptr(50)})

	if !bounds.seen {
		t.Fatal("seen = false, want true")
	}
	if bounds.tStart != 10 {
		t.Fatalf("tStart = %d, want 10", bounds.tStart)
	}
	if bounds.tEnd != 50 {
		t.Fatalf("tEnd = %d, want 50", bounds.tEnd)
	}
}

func timelineInt64Ptr(v int64) *int64 {
	return &v
}

func TestAttachTimelineSpanToLane_AppendsAndSkipsMissingLane(t *testing.T) {
	t.Parallel()

	lanes := []timelineLane{
		{Key: "session:a", Label: "a", Spans: []timelineSpan{}},
	}
	laneIndex := map[string]int{"a": 0}

	if !attachTimelineSpanToLane(lanes, laneIndex, "a", timelineSpan{ID: "op-a", StartTS: 10}) {
		t.Fatal("append known lane = false, want true")
	}
	if len(lanes[0].Spans) != 1 || lanes[0].Spans[0].ID != "op-a" {
		t.Fatalf("known lane spans = %+v, want op-a", lanes[0].Spans)
	}
	if attachTimelineSpanToLane(lanes, laneIndex, "missing", timelineSpan{ID: "op-missing", StartTS: 20}) {
		t.Fatal("append missing lane = true, want false")
	}
	if len(lanes[0].Spans) != 1 {
		t.Fatalf("known lane spans changed after missing lane: %+v", lanes[0].Spans)
	}
}
