package output

import "testing"

func TestTable(t *testing.T) {
	lines := Table([]string{"NAME", "STATE"}, [][]string{{"@dscape/test", "running"}})
	if len(lines) != 2 || lines[0] != "NAME          STATE" || lines[1] != "@dscape/test  running" {
		t.Fatalf("unexpected table: %#v", lines)
	}
}

func TestSimpleDiff(t *testing.T) {
	lines := SimpleDiff([]byte("old\n"), []byte("new\n"))
	if len(lines) != 2 || lines[0] != "-old" || lines[1] != "+new" {
		t.Fatalf("unexpected diff: %#v", lines)
	}
	unchanged := SimpleDiff([]byte("same\n"), []byte("same\n"))
	if len(unchanged) != 1 || unchanged[0] != "  no changes" {
		t.Fatalf("unexpected unchanged diff: %#v", unchanged)
	}
}
