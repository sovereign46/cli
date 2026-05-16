package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sovereign46/s46-cli/internal/harness"
)

type Renderer struct {
	JSON bool
	Out  io.Writer
}

func (r Renderer) WriteJSON(value any) error {
	encoder := json.NewEncoder(r.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (r Renderer) Lines(lines ...string) error {
	_, err := fmt.Fprintln(r.Out, strings.Join(lines, "\n"))
	return err
}

func RenderPlan(plan harness.Plan) []string {
	lines := []string{fmt.Sprintf("[s46] %s", plan.Title), fmt.Sprintf("[s46] %s", plan.Summary)}
	for _, operation := range plan.Operations {
		lines = append(lines, "  - "+operation)
	}
	for _, file := range plan.Files {
		lines = append(lines, "", fmt.Sprintf("--- %s", file.DisplayPath), fmt.Sprintf("+++ %s", file.DisplayPath))
		lines = append(lines, SimpleDiff(file.OldContent, file.Content)...)
	}
	return lines
}

func SimpleDiff(oldContent []byte, newContent []byte) []string {
	if string(oldContent) == string(newContent) {
		return []string{"  no changes"}
	}
	lines := []string{}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	for _, line := range oldLines {
		lines = append(lines, "-"+line)
	}
	for _, line := range newLines {
		lines = append(lines, "+"+line)
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
