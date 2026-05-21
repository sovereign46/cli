package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// DefaultPrefix is the literal CLI prefix used for cloud-mode output
// and is the marker token that production code embeds at the start of
// rendered lines. The renderer translates this marker to whatever
// active prefix is set (e.g. airplane.Prefix), so every brand-aware
// line uses DefaultPrefix and the renderer rewrites at print time.
//
// New code: import this constant rather than hardcoding "[s46]".
const DefaultPrefix = "[s46]"

type Interface interface {
	WriteJSON(value any) error
	WriteJSONL(value any) error
	Lines(lines ...string) error
}

// Renderer prints either JSON, JSONL, or plain lines. Plain output respects
// the active brand prefix: lines that begin with DefaultPrefix have
// it swapped for r.Prefix; lines that begin with the status markers
// "[ok]" or "[fail]" get r.Prefix prepended when r.Prefix differs
// from DefaultPrefix (cloud mode emits bare status lines; airplane
// mode prefixes them to disambiguate from third-party tools).
type Renderer struct {
	JSON   bool
	JSONL  bool
	Out    io.Writer
	Prefix string
}

func (r Renderer) WriteJSON(value any) error {
	encoder := json.NewEncoder(r.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (r Renderer) WriteJSONL(value any) error {
	encoder := json.NewEncoder(r.Out)
	return encoder.Encode(value)
}

func (r Renderer) Lines(lines ...string) error {
	if r.Prefix != "" && r.Prefix != DefaultPrefix {
		lines = rewritePrefix(lines, r.Prefix)
	}
	_, err := fmt.Fprintln(r.Out, strings.Join(lines, "\n"))
	return err
}

func rewritePrefix(lines []string, prefix string) []string {
	rewritten := make([]string, len(lines))
	for i, line := range lines {
		if strings.HasPrefix(line, DefaultPrefix) {
			rewritten[i] = prefix + strings.TrimPrefix(line, DefaultPrefix)
		} else if strings.HasPrefix(line, "[ok]") || strings.HasPrefix(line, "[fail]") {
			rewritten[i] = prefix + " " + line
		} else {
			rewritten[i] = line
		}
	}
	return rewritten
}

func SimpleDiff(oldContent []byte, newContent []byte) []string {
	if string(oldContent) == string(newContent) {
		return []string{"  no changes"}
	}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	lcs := make([][]int, len(oldLines)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	lines := []string{fmt.Sprintf("@@ -1,%d +1,%d @@", len(oldLines), len(newLines))}
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			lines = append(lines, " "+oldLines[i])
			i++
			j++
		case j < len(newLines) && (i == len(oldLines) || lcs[i][j+1] >= lcs[i+1][j]):
			lines = append(lines, "+"+newLines[j])
			j++
		case i < len(oldLines):
			lines = append(lines, "-"+oldLines[i])
			i++
		}
	}
	return lines
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	text := strings.TrimRight(string(content), "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func Table(headers []string, rows [][]string) []string {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	format := func(row []string) string {
		cells := make([]string, len(headers))
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			cells[i] = cell + strings.Repeat(" ", widths[i]-len(cell))
		}
		return strings.TrimRight(strings.Join(cells, "  "), " ")
	}
	lines := []string{format(headers)}
	for _, row := range rows {
		lines = append(lines, format(row))
	}
	return lines
}
