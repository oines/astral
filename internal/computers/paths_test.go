package computers

import "testing"

func TestPathsUseReportedComputerOS(t *testing.T) {
	for _, test := range []struct {
		system, path string
		absolute     bool
	}{
		{"windows", `C:\Users\名字\Project`, true}, {"windows", `c:/Users/name`, true},
		{"windows", `\\host\share\folder`, true}, {"windows", `\\?\C:\long\path`, true}, {"windows", `\\?\UNC\host\share\folder`, true},
		{"windows", `C:relative`, false}, {"windows", `\relative-to-drive`, false}, {"windows", `/unix/path`, false}, {"windows", `\\host`, false},
		{"windows", `\\.\pipe\docker_engine`, false}, {"windows", "C:\\bad\x00path", false},
		{"linux", "/home/name", true}, {"darwin", "/Users/名字", true}, {"linux", `C:\Users\name`, false}, {"linux", "relative", false},
	} {
		if got := absoluteTargetPath(test.system, test.path); got != test.absolute {
			t.Errorf("%s %q: got %v want %v", test.system, test.path, got, test.absolute)
		}
	}
	hello := Hello{BootID: "test", Hostname: "windows-computer", OS: "windows", Home: `C:\Users\名字`, Shell: `C:\Program Files\PowerShell\7\pwsh.exe`, Root: `C:\Users\名字\AppData\Roaming\astral`}
	if err := validateHello(hello); err != nil {
		t.Fatal(err)
	}
	hello.Home = `C:relative`
	if err := validateHello(hello); err == nil {
		t.Fatal("accepted a drive-relative Windows home")
	}
}

func TestEnrollmentShellQuoting(t *testing.T) {
	value := "https://host.example/a'b$(command);`literal`"
	if got := powerShellQuote(value); got != "'https://host.example/a''b$(command);`literal`'" {
		t.Fatal(got)
	}
	if got := shellQuote(value); got != "'https://host.example/a'\\''b$(command);`literal`'" {
		t.Fatal(got)
	}
}
