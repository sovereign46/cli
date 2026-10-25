//go:build release

package cli

import "testing"

func TestReleaseBuildRejectsStatusProbeOverride(t *testing.T) {
	checks, ok := statusProbeOverride(map[string]string{"S46_TEST_STATUS_LIVE_OK": "1"}, "cloud")
	if ok || checks != nil {
		t.Fatalf("release build accepted status probe override: ok=%v checks=%#v", ok, checks)
	}
}
