package platform

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestEmbeddedWebIncludesUnderscoreChunks(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, "server.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var declaration string
	for _, node := range parsed.Decls {
		group, ok := node.(*ast.GenDecl)
		if !ok || group.Tok != token.VAR {
			continue
		}
		for _, spec := range group.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				if name.Name == "embeddedWeb" {
					start := group.Pos()
					if group.Doc != nil {
						start = group.Doc.Pos()
					}
					declaration = string(source[positions.Position(start).Offset:positions.Position(group.End()).Offset])
				}
			}
		}
	}
	if declaration == "" {
		t.Fatal("server.go has no embeddedWeb declaration")
	}
	// Exercise Go's actual file selection with the production declaration, even
	// when this checkout contains only the placeholder web/README.txt. No fixture
	// asset is added to the shipped Web bundle and no application rebuild is needed.
	root := t.TempDir()
	const helper = "web/assets/_plugin-vue_export-helper-fixture.js"
	if err := os.MkdirAll(filepath.Join(root, "web", "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"go.mod":         "module embeddedwebfixture\n\ngo 1.18\n",
		"embedded.go":    "package embeddedwebfixture\n\nimport \"embed\"\n\n" + declaration + "\n",
		"web/index.html": `<script type="module" src="/assets/_plugin-vue_export-helper-fixture.js"></script>`,
		helper:           "export const ready = true;\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-json", ".")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve production Web embeds: %v\n%s", err, output)
	}
	var result struct{ EmbedFiles []string }
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Go embed selection: %v\n%s", err, output)
	}
	if !slices.Contains(result.EmbedFiles, helper) {
		t.Fatalf("production Web embed omitted required Vite chunk %q; selected files: %v", helper, result.EmbedFiles)
	}
}
