//go:build !release

package session

func mockShareBackend(env map[string]string) (bool, error) {
	return env["S46_SHARE_BACKEND"] == "mock", nil
}
