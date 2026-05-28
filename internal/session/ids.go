package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var (
	userSlugSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	taskSlugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)
)

func IDForTask(user string, task string) string {
	name := "user"
	if user != "" {
		if extracted := userSlugSanitizer.ReplaceAllString(strings.Split(user, "@")[0], ""); extracted != "" {
			name = extracted
		}
	}
	slug := taskSlugSanitizer.ReplaceAllString(strings.ToLower(task), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		slug = "session"
	}
	return fmt.Sprintf("@%s/%s-%s", name, slug, secureToken(5))
}

func secureToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "0000000000"
	}
	return hex.EncodeToString(buf)
}
