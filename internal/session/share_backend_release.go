//go:build release

package session

import "errors"

func mockShareBackend(env map[string]string) (bool, error) {
	if env["S46_SHARE_BACKEND"] == "mock" {
		return false, errors.New("mock share backend is unavailable in release builds")
	}
	return false, nil
}
