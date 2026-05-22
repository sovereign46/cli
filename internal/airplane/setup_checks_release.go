//go:build release

package airplane

func (Service) setupChecksSkipped() bool { return false }

// SetupChecksSkipped is false in release builds; runtime signature and
// checksum checks cannot be disabled by environment variables.
func (Service) SetupChecksSkipped() bool { return false }
