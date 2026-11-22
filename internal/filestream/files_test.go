package filestream

import "testing"

func TestPathURLEscapesNativePathOnce(t *testing.T) {
	// The Web's file preview decodes the same Windows path from assistant Markdown.
	path := `C:\报告\a+b %2F #.png`
	want := "astral-file://PC_MiX?path=C%3A%5C%E6%8A%A5%E5%91%8A%5Ca%2Bb+%252F+%23.png"
	if got := PathURL("PC_MiX", path); got != want {
		t.Fatalf("encoded URI %q, want %q", got, want)
	}
}
