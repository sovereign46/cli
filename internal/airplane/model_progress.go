package airplane

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sovereign46/cli/internal/models"
	"github.com/sovereign46/cli/internal/strs"
)

const (
	modelInstallProgressBarWidth    = 24
	modelInstallProgressMinBarWidth = 8
	modelInstallProgressEvery       = 250 * time.Millisecond
)

type modelInstallProgressRenderer struct {
	out         io.Writer
	prefix      string
	lineWidth   int
	phase       models.InstallProgressPhase
	startedAt   time.Time
	lastUpdate  time.Time
	lastLineLen int
}

func (s Service) modelInstallProgress() models.InstallProgressFunc {
	if s.Progress == nil {
		return nil
	}
	renderer := &modelInstallProgressRenderer{out: s.Progress, prefix: s.logPrefix(), lineWidth: modelInstallProgressLineWidth(s.Progress, s.Env)}
	return renderer.Update
}

func (r *modelInstallProgressRenderer) Update(progress models.InstallProgress) {
	if r.out == nil || progress.Total <= 0 {
		return
	}
	now := time.Now()
	if r.startedAt.IsZero() || r.phase != progress.Phase {
		if r.lastLineLen > 0 {
			_, _ = fmt.Fprintln(r.out)
		}
		r.phase = progress.Phase
		r.startedAt = now
		r.lastUpdate = time.Time{}
		r.lastLineLen = 0
	}
	if !progress.Done && !r.lastUpdate.IsZero() && now.Sub(r.lastUpdate) < modelInstallProgressEvery {
		return
	}
	r.lastUpdate = now
	line := r.format(progress, now)
	lineLen := progressLineVisibleWidth(line)
	clear := ""
	if r.lastLineLen > lineLen {
		clear = strings.Repeat(" ", r.lastLineLen-lineLen)
	}
	_, _ = fmt.Fprintf(r.out, "\r%s%s", line, clear)
	if progress.Done {
		_, _ = fmt.Fprintln(r.out)
		r.startedAt = time.Time{}
		r.lastUpdate = time.Time{}
		r.lastLineLen = 0
		return
	}
	r.lastLineLen = lineLen
}

func (r *modelInstallProgressRenderer) format(progress models.InstallProgress, now time.Time) string {
	current := clampProgress(progress.Current, progress.Total)
	percent := float64(current) / float64(progress.Total) * 100
	return formatModelInstallProgressLine(modelInstallProgressLine{
		Prefix:  r.prefix,
		Verb:    modelInstallProgressVerb(progress.Phase),
		Name:    modelInstallProgressName(progress),
		Percent: fmt.Sprintf("%3.0f%%", percent),
		Bytes:   fmt.Sprintf("%s/%s", formatProgressBytes(current), formatProgressBytes(progress.Total)),
		Suffix:  r.progressSuffix(current, progress.Total, progress.Done, now),
		Current: current,
		Total:   progress.Total,
		Width:   safeModelInstallProgressLineWidth(r.lineWidth),
	})
}

func modelInstallProgressLineWidth(out io.Writer, env map[string]string) int {
	if width := terminalWidth(out); width > 0 {
		return width
	}
	width, err := strconv.Atoi(strings.TrimSpace(strs.EnvValue(env, "COLUMNS")))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

func safeModelInstallProgressLineWidth(width int) int {
	if width <= 1 {
		return 0
	}
	return width - 1
}

type modelInstallProgressLine struct {
	Prefix  string
	Verb    string
	Name    string
	Percent string
	Bytes   string
	Suffix  string
	Current int64
	Total   int64
	Width   int
}

func formatModelInstallProgressLine(line modelInstallProgressLine) string {
	if rendered := buildModelInstallProgressLine(line, modelInstallProgressBarWidth, line.Name, line.Suffix); line.Width <= 0 || progressLineVisibleWidth(rendered) <= line.Width {
		return rendered
	}
	if rendered, ok := fitModelInstallProgressName(line, modelInstallProgressBarWidth, line.Suffix); ok {
		return rendered
	}
	if rendered, ok := fitModelInstallProgressName(line, modelInstallProgressBarWidth, ""); ok {
		return rendered
	}
	for barWidth := modelInstallProgressBarWidth - 1; barWidth >= modelInstallProgressMinBarWidth; barWidth-- {
		if rendered, ok := fitModelInstallProgressName(line, barWidth, ""); ok {
			return rendered
		}
	}
	return truncateProgressText(buildModelInstallProgressLine(line, modelInstallProgressMinBarWidth, "", ""), line.Width)
}

func fitModelInstallProgressName(line modelInstallProgressLine, barWidth int, suffix string) (string, bool) {
	available := line.Width - progressLineVisibleWidth(buildModelInstallProgressLine(line, barWidth, "", suffix))
	if available < 0 {
		return "", false
	}
	name := truncateProgressText(line.Name, available)
	rendered := buildModelInstallProgressLine(line, barWidth, name, suffix)
	return rendered, progressLineVisibleWidth(rendered) <= line.Width
}

func buildModelInstallProgressLine(line modelInstallProgressLine, barWidth int, name string, suffix string) string {
	parts := []string{
		line.Prefix,
		line.Verb,
		name,
		line.Percent,
		"[" + modelInstallProgressBarWithWidth(line.Current, line.Total, barWidth) + "]",
		line.Bytes,
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, " ")
}

func truncateProgressText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if progressLineVisibleWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	keep := width - 1
	front := (keep + 1) / 2
	back := keep / 2
	return string(runes[:front]) + "…" + string(runes[len(runes)-back:])
}

func progressLineVisibleWidth(text string) int {
	return utf8.RuneCountInString(text)
}

func (r *modelInstallProgressRenderer) progressSuffix(current int64, total int64, done bool, now time.Time) string {
	elapsed := now.Sub(r.startedAt)
	if elapsed <= 0 {
		return ""
	}
	if done {
		if current >= total {
			return "in " + formatProgressDuration(elapsed)
		}
		return ""
	}
	if current <= 0 || current >= total {
		return ""
	}
	rate := float64(current) / elapsed.Seconds()
	if rate <= 0 {
		return ""
	}
	eta := time.Duration(float64(total-current) / rate * float64(time.Second))
	return fmt.Sprintf("%s/s ETA %s", formatProgressBytes(int64(rate)), formatProgressDuration(eta))
}

func modelInstallProgressVerb(phase models.InstallProgressPhase) string {
	switch phase {
	case models.InstallProgressDownloading:
		return "downloading"
	case models.InstallProgressVerifying:
		return "verifying"
	default:
		return "working on"
	}
}

func modelInstallProgressName(progress models.InstallProgress) string {
	if strings.TrimSpace(progress.Filename) != "" {
		return progress.Filename
	}
	if strings.TrimSpace(progress.Path) != "" {
		return filepath.Base(progress.Path)
	}
	return strs.FirstNonEmpty(progress.ModelID, "model")
}

func modelInstallProgressBar(current int64, total int64) string {
	return modelInstallProgressBarWithWidth(current, total, modelInstallProgressBarWidth)
}

func modelInstallProgressBarWithWidth(current int64, total int64, width int) string {
	current = clampProgress(current, total)
	if width < 1 {
		width = 1
	}
	plane := 0
	if total > 0 {
		plane = int((current*int64(width-1) + total/2) / total)
	}
	return strings.Repeat("━", plane) + "✈" + strings.Repeat("─", width-plane-1)
}

func clampProgress(current int64, total int64) int64 {
	if current < 0 {
		return 0
	}
	if total > 0 && current > total {
		return total
	}
	return current
}

func formatProgressBytes(bytes int64) string {
	if bytes < 1000 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(bytes)
	unit := "B"
	for _, next := range units {
		value /= 1000
		unit = next
		if value < 1000 {
			break
		}
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func formatProgressDuration(duration time.Duration) string {
	rounded := duration.Round(time.Second)
	if rounded < time.Second {
		return "<1s"
	}
	return formatDuration(rounded)
}
