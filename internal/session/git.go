package session

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/sovereign46/cli/internal/contextx"
)

func gitOutput(ctx context.Context, args ...string) string {
	commandCtx, cancel := contextx.WithMaxTimeout(ctx, contextx.DefaultCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Env = withoutRepositoryGitEnvironment(os.Environ())
	raw, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// Git exports repository-local variables to hooks. They must not redirect a
// lookup explicitly scoped with -C to the parent repository running the CLI.
func withoutRepositoryGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, binding := range environment {
		name, _, _ := strings.Cut(binding, "=")
		if repositoryGitEnvironment[name] {
			continue
		}
		result = append(result, binding)
	}
	return result
}

var repositoryGitEnvironment = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_DIR":                          true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_INDEX_FILE":                   true,
	"GIT_INTERNAL_SUPER_PREFIX":        true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_PREFIX":                       true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_WORK_TREE":                    true,
}
