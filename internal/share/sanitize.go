package share

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type Redactor struct {
	Home string
}

const redacted = "[redacted]"

var (
	privateKeyBlock = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerToken     = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+\-/]+=*`)
	secretAssign    = regexp.MustCompile(`(?i)\b([A-Z0-9_./-]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API[_-]?KEY|ACCESS[_-]?KEY|AUTHORIZATION)[A-Z0-9_./-]*)\s*[:=]\s*([^\s,;]+)`)
	commonSecret    = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16})\b`)
	controlChars    = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
)

func SanitizeArtifact(artifact Artifact, redactor Redactor) Artifact {
	artifact.Session.ID = redactor.String(artifact.Session.ID)
	artifact.Session.Title = redactor.String(artifact.Session.Title)
	artifact.Session.Task = redactor.String(artifact.Session.Task)
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
		artifact.Steps[i].Body = redactor.String(artifact.Steps[i].Body)
		artifact.Steps[i].Cmd = redactor.String(artifact.Steps[i].Cmd)
		artifact.Steps[i].CWD = redactor.String(artifact.Steps[i].CWD)
		artifact.Steps[i].Out = redactor.String(artifact.Steps[i].Out)
		artifact.Steps[i].Path = redactor.String(artifact.Steps[i].Path)
		artifact.Steps[i].Before = redactor.String(artifact.Steps[i].Before)
		artifact.Steps[i].After = redactor.String(artifact.Steps[i].After)
		for h := range artifact.Steps[i].Hunks {
			artifact.Steps[i].Hunks[h].Header = redactor.String(artifact.Steps[i].Hunks[h].Header)
			for l := range artifact.Steps[i].Hunks[h].Lines {
				artifact.Steps[i].Hunks[h].Lines[l].K = redactor.String(artifact.Steps[i].Hunks[h].Lines[l].K)
				artifact.Steps[i].Hunks[h].Lines[l].V = redactor.String(artifact.Steps[i].Hunks[h].Lines[l].V)
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
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	return value
}
