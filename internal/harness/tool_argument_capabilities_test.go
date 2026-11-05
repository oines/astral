package harness

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/oines/astral-core"
)

func TestHostStandardToolArgumentPreparationCapabilities(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"read": false, "write": false, "edit": true, "exec_command": false, "write_stdin": false}
	seen := 0
	for _, tool := range r.hostTools() {
		name := tool.Definition().Name
		prepares, standard := want[name]
		if !standard {
			continue
		}
		seen++
		preparer, advertised := tool.(core.ToolArgumentPreparer)
		if advertised != prepares {
			t.Errorf("%s advertises PrepareArguments=%v; want %v", name, advertised, prepares)
		}
		if _, targets := tool.(core.TargetedTool); !targets {
			t.Errorf("%s lost Host binding/permission routing", name)
		}
		if name == "edit" && advertised {
			const raw = `{"path":"file","oldText":"before","newText":"after"}`
			prepared, err := preparer.PrepareArguments(json.RawMessage(raw))
			var actual struct {
				Path  string                              `json:"path"`
				Edits []struct{ OldText, NewText string } `json:"edits"`
			}
			if err != nil || json.Unmarshal(prepared, &actual) != nil || actual.Path != "file" || len(actual.Edits) != 1 || actual.Edits[0].OldText != "before" || actual.Edits[0].NewText != "after" {
				t.Fatalf("Host edit wrapper did not forward compatibility preparation: %s %v", prepared, err)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("Host wrapped %d standard tools, want %d", seen, len(want))
	}
}
