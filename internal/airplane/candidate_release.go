//go:build release

package airplane

func CandidateSupported() bool { return false }

func (Service) candidateMode() bool { return false }
