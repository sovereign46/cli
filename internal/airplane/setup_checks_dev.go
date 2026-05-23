//go:build !release

package airplane

import (
	"github.com/sovereign46/cli/internal/strs"
)

func (s Service) setupChecksSkipped() bool {
	return strs.Truthy(strs.EnvValue(s.Env, "S46_AIRPLANE_SKIP_SETUP_CHECKS"))
}

// SetupChecksSkipped exposes the dev/test setup-check bypass to callers that
// need to mirror Service behavior. Release builds always return false.
func (s Service) SetupChecksSkipped() bool {
	return s.setupChecksSkipped()
}
