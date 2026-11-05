//go:build !release

package airplane

import "github.com/sovereign46/cli/internal/strs"

func CandidateSupported() bool { return true }

func (s Service) candidateMode() bool {
	return strs.Truthy(strs.EnvValue(s.Env, "S46_AIRPLANE_CANDIDATE"))
}
