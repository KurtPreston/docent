package server

import (
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/report"
)

func TestReportStoreSubscribeReplayAndFanout(t *testing.T) {
	st := newReportStore()
	id := st.start()
	st.markRunning(id)

	st.emit(id, reportEvent{Type: "phase", Phase: "collecting"})
	st.emit(id, reportEvent{Type: "token", Text: "Hello"})

	replay, ch, cancel, ok := st.subscribe(id)
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer cancel()
	if len(replay) != 2 {
		t.Fatalf("replay len=%d want 2", len(replay))
	}
	if replay[0].Phase != "collecting" || replay[1].Text != "Hello" {
		t.Fatalf("replay=%+v", replay)
	}

	st.emit(id, reportEvent{Type: "token", Text: " world"})
	select {
	case ev := <-ch:
		if ev.Type != "token" || ev.Text != " world" {
			t.Fatalf("live event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}

	view, ok := st.get(id)
	if !ok {
		t.Fatal("get failed")
	}
	if view.Partial != "Hello world" {
		t.Fatalf("partial=%q want %q", view.Partial, "Hello world")
	}
	if view.Phase != "collecting" {
		t.Fatalf("phase=%q", view.Phase)
	}

	st.finish(id, report.Result{
		Markdown: "# done\n",
		Run:      executionmode.ResolvedRun{ModeID: "recent-activity", ModeName: "Recent", Scope: executionmode.ScopeInvolved, LookbackDays: 7},
		Statuses: 0,
	})

	select {
	case ev := <-ch:
		if ev.Type != "done" || ev.Markdown != "# done\n" {
			t.Fatalf("done event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done")
	}

	// Late subscriber gets replay only (channel nil).
	replay2, ch2, cancel2, ok := st.subscribe(id)
	if !ok {
		t.Fatal("late subscribe failed")
	}
	defer cancel2()
	if ch2 != nil {
		t.Fatal("expected nil channel for terminal job")
	}
	foundDone := false
	for _, ev := range replay2 {
		if ev.Type == "done" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("late replay missing done: %+v", replay2)
	}
}

func TestReportStoreFailEmitsError(t *testing.T) {
	st := newReportStore()
	id := st.start()
	_, ch, cancel, ok := st.subscribe(id)
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer cancel()

	st.fail(id, errString("boom"))
	select {
	case ev := <-ch:
		if ev.Type != "error" || ev.Error != "boom" {
			t.Fatalf("error event=%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error")
	}
	view, _ := st.get(id)
	if view.Status != string(reportError) || view.Error != "boom" {
		t.Fatalf("view=%+v", view)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
