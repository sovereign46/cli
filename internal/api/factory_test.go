package api

import (
	"context"
	"testing"
)

func TestDevShellUsesLocalMockURLs(t *testing.T) {
	client := NewClientFromEnv(map[string]string{"S46_DEV_SHELL": "1", "S46_DEV_BASE_URL": "http://127.0.0.1:8080"})
	device, err := client.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.VerificationURI != "http://127.0.0.1:8080/device" {
		t.Fatalf("verification URI = %q", device.VerificationURI)
	}
	team, err := client.Team(context.Background(), "acme", TeamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != "http://127.0.0.1:8080" {
		t.Fatalf("team endpoint = %q", team.Endpoint)
	}
	sessions, err := client.Sessions(context.Background(), team, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Location != "127.0.0.1:8080" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestDefaultMockUsesProductionURLs(t *testing.T) {
	client := NewClientFromEnv(nil)
	device, err := client.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.VerificationURI != "https://s46.dev/device" {
		t.Fatalf("verification URI = %q", device.VerificationURI)
	}
	team, err := client.Team(context.Background(), "acme", TeamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if team.Endpoint != "https://acme.s46.dev" {
		t.Fatalf("team endpoint = %q", team.Endpoint)
	}
}
