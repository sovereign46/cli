package share

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type Redactor struct {
	Home string
}

const (
	redacted          = "[redacted]"
	maxShareTextBytes = 8 * 1024
	maxShareToolBytes = 4 * 1024
	maxShareDiffBytes = 1024
)

var (
	privateKeyBlock      = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerToken          = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	secretAssign         = regexp.MustCompile(`(?i)\b([A-Z0-9_./-]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API[_-]?KEY|ACCESS[_-]?KEY|AUTHORIZATION)[A-Z0-9_./-]*)\s*[:=]\s*([^\s,;]+)`)
	commonSecret         = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16})\b`)
	reasoningDoubleValue = regexp.MustCompile(`(?is)((?:"|')(?:thinking|thinkingSignature|encrypted_content)(?:"|')\s*:\s*)"(?:\\.|[^"\\])*"`)
	reasoningSingleValue = regexp.MustCompile(`(?is)((?:"|')(?:thinking|thinkingSignature|encrypted_content)(?:"|')\s*:\s*)'(?:\\.|[^'\\])*'`)
	controlChars         = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
)

func SanitizeArtifact(artifact Artifact, redactor Redactor) Artifact {
	artifact.Session.ID = redactor.String(artifact.Session.ID)
	artifact.Session.Title = redactor.String(artifact.Session.Title)
	artifact.Session.Task = redactor.Text(artifact.Session.Task)
	artifact.Session.Status = redactor.String(artifact.Session.Status)
	artifact.Session.Location = redactor.String(artifact.Session.Location)
	artifact.Session.Team = redactor.String(artifact.Session.Team)
	artifact.Session.Visibility = redactor.String(artifact.Session.Visibility)
	artifact.Session.SharedAt = redactor.String(artifact.Session.SharedAt)
	artifact.Session.SharedBy.Handle = redactor.String(artifact.Session.SharedBy.Handle)
	artifact.Session.SharedBy.Email = redactor.String(artifact.Session.SharedBy.Email)
	artifact.Session.Harness.Name = redactor.String(artifact.Session.Harness.Name)
	artifact.Session.Harness.Version = redactor.String(artifact.Session.Harness.Version)
	artifact.Session.Model.Name = redactor.String(artifact.Session.Model.Name)
	artifact.Session.Lane.ID = redactor.String(artifact.Session.Lane.ID)
	for i := range artifact.Steps {
		artifact.Steps[i].Kind = redactor.String(artifact.Steps[i].Kind)
		artifact.Steps[i].Body = redactor.StepText(artifact.Steps[i].Kind, artifact.Steps[i].Body)
		artifact.Steps[i].Cmd = redactor.String(artifact.Steps[i].Cmd)
		artifact.Steps[i].CWD = redactor.String(artifact.Steps[i].CWD)
		artifact.Steps[i].Out = redactor.Tool(artifact.Steps[i].Out)
		artifact.Steps[i].Path = redactor.String(artifact.Steps[i].Path)
		artifact.Steps[i].Before = redactor.Tool(artifact.Steps[i].Before)
		artifact.Steps[i].After = redactor.Tool(artifact.Steps[i].After)
		for h := range artifact.Steps[i].Hunks {
			artifact.Steps[i].Hunks[h].Header = redactor.String(artifact.Steps[i].Hunks[h].Header)
			for l := range artifact.Steps[i].Hunks[h].Lines {
				artifact.Steps[i].Hunks[h].Lines[l].K = redactor.String(artifact.Steps[i].Hunks[h].Lines[l].K)
				artifact.Steps[i].Hunks[h].Lines[l].V = redactor.Diff(artifact.Steps[i].Hunks[h].Lines[l].V)
			}
		}
	}
	for i := range artifact.Files {
		artifact.Files[i].Path = redactor.String(artifact.Files[i].Path)
		artifact.Files[i].Op = redactor.String(artifact.Files[i].Op)
	}
	if artifact.Review != nil {
		artifact.Review.Summary = redactor.String(artifact.Review.Summary)
		for i := range artifact.Review.Checklist {
			artifact.Review.Checklist[i] = redactor.String(artifact.Review.Checklist[i])
		}
		for i := range artifact.Review.SuggestedCommands {
			artifact.Review.SuggestedCommands[i] = redactor.String(artifact.Review.SuggestedCommands[i])
		}
	}
	return artifact
}

func (r Redactor) String(value string) string {
	return r.string(value, 0)
}

func (r Redactor) Text(value string) string {
	return r.string(value, maxShareTextBytes)
}

func (r Redactor) Tool(value string) string {
	return r.string(value, maxShareToolBytes)
}

func (r Redactor) Diff(value string) string {
	return r.string(value, maxShareDiffBytes)
}

func (r Redactor) StepText(kind string, value string) string {
	switch kind {
	case "read", "bash", "edit":
		return r.Tool(value)
	default:
		return r.Text(value)
	}
}

func (r Redactor) string(value string, maxBytes int) string {
	if value == "" {
		return ""
	}
	value = strings.ToValidUTF8(value, "")
	value = controlChars.ReplaceAllString(value, "")
	if r.Home != "" {
		value = strings.ReplaceAll(value, r.Home, "~")
	}
	value = privateKeyBlock.ReplaceAllString(value, redacted)
	value = bearerToken.ReplaceAllString(value, "Bearer "+redacted)
	value = secretAssign.ReplaceAllString(value, `$1=`+redacted)
	value = commonSecret.ReplaceAllString(value, redacted)
	value = reasoningDoubleValue.ReplaceAllString(value, `${1}"`+redacted+`"`)
	value = reasoningSingleValue.ReplaceAllString(value, `${1}'`+redacted+`'`)
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	if maxBytes > 0 {
		value = truncateUTF8(value, maxBytes)
	}
	return value
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	suffix := "\n[truncated]"
	limit := maxBytes - len(suffix)
	if limit <= 0 {
		return suffix
	}
	truncated := value[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + suffix
}
