package harness

import (
	"context"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

func TestBoundWebInputsRespectRequestedMode(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		kind core.InputKind
	}{
		{name: "default", kind: core.InputFollowUp},
		{name: "follow_up", mode: "follow_up", kind: core.InputFollowUp},
		{name: "steer", mode: "steer", kind: core.InputSteer},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			f := newFixture(t, 0, func(ctx context.Context, n int, request map[string]any) any {
				if n == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil
					}
				} else if strings.Count(string(encode(request)), "Independent Web task") != 1 {
					t.Error("the queued Web prompt was omitted or duplicated")
				}
				return textReply("Finished.")
			})
			bindPromptFixture(t, f, promptFixtureChat(t, f, "bound-chat"))
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "active", Text: "Initial work"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			input := InputRequest{ID: "web-next", Text: "Independent Web task", Mode: test.mode}
			first, err := f.service.Submit(f.ctx, f.session.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			session, err := f.service.Core(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := session.LookupInput(f.ctx, input.ID)
			if err != nil || receipt.Kind != test.kind || receipt.State != core.InputPending {
				t.Fatalf("bound Web input changed mode or applied early: %+v, %v", receipt, err)
			}
			if session.State().PendingInputs[test.kind] != 1 || receipt.Message == nil || receipt.Message.ContextKind != "" || len(receipt.Contexts) != 0 {
				t.Fatalf("Web input acquired an IM observation or wrong queue: %+v", receipt)
			}
			retry, err := f.service.Submit(f.ctx, f.session.ID, input)
			if err != nil || retry.InputID != first.InputID || session.State().PendingInputs[test.kind] != 1 {
				t.Fatalf("retry duplicated the pending Web input: %+v, %v", retry, err)
			}
			close(release)
			f.idle(t, f.session.ID)
			if f.count() != 2 {
				t.Fatalf("Web input did not drive exactly one subsequent request: %d", f.count())
			}
		})
	}
}
