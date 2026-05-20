package strs

import "testing"

func TestTruthy(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"no":    false,
		"NO":    false,
		"off":   false,
		"OFF":   false,
		" 0 ":   false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true,
		"on":    true,
		"  ":    false,
		"x":     true,
	}
	for input, want := range cases {
		if got := Truthy(input); got != want {
			t.Errorf("Truthy(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()
	if got := FirstNonEmpty("", "  ", "first", "second"); got != "first" {
		t.Errorf("got %q, want %q", got, "first")
	}
	if got := FirstNonEmpty("", "  "); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := FirstNonEmpty("  padded  ", "other"); got != "padded" {
		t.Errorf("got %q, want %q", got, "padded")
	}
}

func TestEnvValue(t *testing.T) {
	env := map[string]string{"FOO": "bar"}
	if got := EnvValue(env, "FOO"); got != "bar" {
		t.Errorf("EnvValue(map, FOO) = %q, want bar", got)
	}
	if got := EnvValue(env, "MISSING"); got != "" {
		t.Errorf("EnvValue(map, MISSING) = %q, want empty", got)
	}
	t.Setenv("S46_STRS_TEST_KEY", "set-from-env")
	if got := EnvValue(nil, "S46_STRS_TEST_KEY"); got != "set-from-env" {
		t.Errorf("EnvValue(nil, key) = %q, want set-from-env", got)
	}
}
