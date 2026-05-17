package output

import "testing"

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
