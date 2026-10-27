package daemonservice

import (
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWindowsUpdateQueryAcceptsMissingTask(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows PowerShell and Task Scheduler")
	}
	m, _, _ := serviceFixture(t, "windows")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Query only a unique nonexistent task; never register, stop or delete one.
	script := windowsUpdateTaskQuery("Astral update probe (" + m.id + ")")
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	started := time.Now()
	output, err := command.CombinedOutput()
	elapsed := time.Since(started)
	exitCode := -1
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	if err != nil {
		t.Fatalf("missing update task must be a successful empty query: elapsed=%s ctx.Err=%v exit=%d err=%v output=%q", elapsed, ctx.Err(), exitCode, err, output)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("expected no matching task: elapsed=%s ctx.Err=%v exit=%d output=%q", elapsed, ctx.Err(), exitCode, output)
	}
	t.Logf("missing update task query completed: elapsed=%s exit=%d", elapsed, exitCode)
}

func TestUpdateHelperUsesIndependentUserJob(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			m, spec, fake := serviceFixture(t, platform)
			var taskXML string
			original := m.run
			m.run = func(ctx context.Context, name string, args ...string) (commandResult, error) {
				if name == "schtasks.exe" && len(args) > 2 && args[0] == "/create" {
					raw, err := os.ReadFile(args[2])
					if err != nil {
						t.Fatal(err)
					}
					for i := 2; i+1 < len(raw); i += 2 {
						taskXML += string(rune(raw[i]) | rune(raw[i+1])<<8)
					}
				}
				return original(ctx, name, args...)
			}
			if err := m.StartUpdate(context.Background(), spec.Executable); err != nil {
				t.Fatal(err)
			}
			m.FinishUpdate(context.Background())
			calls := strings.Join(fake.calls, "\n")
			for _, forbidden := range []string{"sudo", "--system", "highest", "--token", "--server", "KillMode=process"} {
				if strings.Contains(calls, forbidden) {
					t.Fatalf("unsafe updater: %s", calls)
				}
			}
			if !strings.Contains(calls, "--managed-helper") && platform != "windows" {
				t.Fatal("helper did not use managed cleanup")
			}
			switch platform {
			case "linux":
				if !strings.Contains(calls, "systemd-run --user --collect") {
					t.Fatal(calls)
				}
			case "darwin":
				if !strings.Contains(calls, "submit -l app.astral.daemon.update.") || !strings.Contains(calls, "remove app.astral.daemon.update.") {
					t.Fatal(calls)
				}
			case "windows":
				if !strings.Contains(taskXML, "InteractiveToken") || !strings.Contains(taskXML, "LeastPrivilege") || !strings.Contains(taskXML, "<Hidden>false</Hidden>") || !strings.Contains(taskXML, "DeleteExpiredTaskAfter") || !strings.Contains(calls, "/delete /tn \\Astral\\Astral Daemon update") {
					t.Fatalf("invalid user update task: %s %s", taskXML, calls)
				}
				// triggerBaseType is an XSD sequence. Well-formed XML alone
				// would miss the ordering error rejected by Task Scheduler.
				var definition struct {
					Triggers struct {
						TimeTrigger struct {
							Children []struct{ XMLName xml.Name } `xml:",any"`
						} `xml:"TimeTrigger"`
					} `xml:"Triggers"`
				}
				decoded := strings.Replace(taskXML, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
				if err := xml.Unmarshal([]byte(decoded), &definition); err != nil {
					t.Fatal(err)
				}
				var fields []string
				for _, child := range definition.Triggers.TimeTrigger.Children {
					fields = append(fields, child.XMLName.Local)
				}
				if !slices.Equal(fields, []string{"Enabled", "StartBoundary", "EndBoundary"}) {
					t.Fatalf("Windows update trigger violates XSD sequence: %v", fields)
				}
			}
		})
	}
}
