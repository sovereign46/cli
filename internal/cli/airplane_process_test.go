package cli

import "testing"

func TestLocalServerPort(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080": "8080",
		"http://localhost":      "80",
		"https://localhost":     "443",
		"://bad-url":            "",
	}
	for rawURL, want := range cases {
		if got := localServerPort(rawURL); got != want {
			t.Fatalf("localServerPort(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

func TestCanRestartAirplaneGatewayOnlyAllowsS46GatewayProcesses(t *testing.T) {
	if !canRestartAirplaneGateway(listeningProcessStatus{Status: "listening", PID: "123", Command: "/usr/local/bin/s46-gateway"}) {
		t.Fatal("expected s46-gateway listener to be restartable")
	}
	if canRestartAirplaneGateway(listeningProcessStatus{Status: "listening", PID: "123", Command: "node server.js"}) {
		t.Fatal("expected non-s46 listener not to be restartable")
	}
	if canRestartAirplaneGateway(listeningProcessStatus{Status: "unknown", PID: "123", Command: "s46-gateway"}) {
		t.Fatal("expected unknown listener status not to be restartable")
	}
	if canRestartAirplaneGateway(listeningProcessStatus{Status: "listening", Command: "s46-gateway"}) {
		t.Fatal("expected listener without pid not to be restartable")
	}
}
