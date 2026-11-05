package harness

import (
	"context"
	"testing"
)

func TestOwnerModelChangeResumesIMAfterSelectionCommitOutsideRuntimeLock(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		return textReply("No generation is needed for selection.")
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &imResumeRecorder{runtime: r}
	f.service.IM = recorder
	if err := f.service.SelectModel(f.ctx, r.id, f.agent.DefaultModelID); err != nil {
		t.Fatal(err)
	}
	if recorder.count.Load() != 0 {
		t.Fatal("selecting the same model forced a paused inbox retry")
	}
	target := subagentTreeModel(t, f)
	if err := f.service.SelectModel(f.ctx, r.id, target.ID); err != nil {
		t.Fatal(err)
	}
	selection, err := f.service.ModelSelection(f.ctx, r.id)
	if err != nil || selection.ID != target.ID || recorder.count.Load() != 1 || recorder.locked.Load() {
		t.Fatalf("changed selection did not resume after commit outside the runtime lock: selection=%+v count=%d locked=%v err=%v", selection, recorder.count.Load(), recorder.locked.Load(), err)
	}
	if err := f.service.SelectModel(f.ctx, r.id, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SelectModel(f.ctx, r.id, "missing-model"); err == nil {
		t.Fatal("missing model selection succeeded")
	}
	if recorder.count.Load() != 1 || f.count() != 0 {
		t.Fatalf("a repeated or failed selection resumed IM or started a model: resumes=%d requests=%d", recorder.count.Load(), f.count())
	}
}
