//go:build release

package api

import "errors"

// ErrMockUnavailable is returned when S46_API_MODE=mock is requested in a
// build that does not include the mock API fixtures.
var ErrMockUnavailable = errors.New("mock API mode is unavailable in this build")

func newMockClientFromEnv(map[string]string) (Client, error) {
	return nil, ErrMockUnavailable
}
