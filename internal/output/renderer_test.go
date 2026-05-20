package output

import (
	"bytes"
	"strings"
	"testing"
)

// TestDefaultPrefixMarker pins the literal cloud-mode prefix. If this
// is changed, every line in the codebase that embeds "[s46]" must be
// reviewed for rebrand drift.
func TestDefaultPrefixMarker(t *testing.T) {
	t.Parallel()
	if DefaultPrefix != "[s46]" {
		t.Fatalf("DefaultPrefix changed; rebrand requires touching all line emitters: got %q", DefaultPrefix)
	}
}

func TestRendererRewritesDefaultPrefix(t *testing.T) {
	t.Parallel()
	// With a non-default prefix, lines starting with DefaultPrefix are
	// swapped; [ok]/[fail] status lines get the prefix prepended.
	buf := &bytes.Buffer{}
	r := Renderer{Out: buf, Prefix: "[s46airplane]"}
	if err := r.Lines("[s46] team: acme", "[ok] ready", "  - indented"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"[s46airplane] team: acme",
		"[s46airplane] [ok] ready",
		"  - indented",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in rendered output:\n%s", want, got)
		}
	}
}

func TestRendererPassThroughForDefaultPrefix(t *testing.T) {
	t.Parallel()
	// Empty Prefix and DefaultPrefix both leave lines unchanged — this
	// is the cloud-mode contract where "[ok]" stays bare.
	for _, prefix := range []string{"", DefaultPrefix} {
		buf := &bytes.Buffer{}
		r := Renderer{Out: buf, Prefix: prefix}
		if err := r.Lines("[s46] hello", "[ok] world"); err != nil {
			t.Fatal(err)
		}
		got := buf.String()
		if !strings.Contains(got, "[s46] hello") || !strings.Contains(got, "[ok] world") {
			t.Fatalf("Prefix=%q rendered %q (expected pass-through)", prefix, got)
		}
		// Ensure the renderer did NOT add an extra prefix to [ok] lines.
		if strings.Contains(got, "[s46] [ok]") {
			t.Fatalf("Prefix=%q rendered %q (cloud mode should not double-prefix sub-bullets)", prefix, got)
		}
	}
}

func TestTable(t *testing.T) {
	lines := Table([]string{"NAME", "STATE"}, [][]string{{"@dscape/test", "running"}})
	if len(lines) != 2 || lines[0] != "NAME          STATE" || lines[1] != "@dscape/test  running" {
		t.Fatalf("unexpected table: %#v", lines)
	}
}

func TestSimpleDiff(t *testing.T) {
	lines := SimpleDiff([]byte("keep\nold\n"), []byte("keep\nnew\n"))
	if len(lines) != 4 || lines[0] != "@@ -1,2 +1,2 @@" || lines[1] != " keep" || lines[2] != "+new" || lines[3] != "-old" {
		t.Fatalf("unexpected diff: %#v", lines)
	}
	unchanged := SimpleDiff([]byte("same\n"), []byte("same\n"))
	if len(unchanged) != 1 || unchanged[0] != "  no changes" {
		t.Fatalf("unexpected unchanged diff: %#v", unchanged)
	}
}
