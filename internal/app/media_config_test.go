package app

import "testing"

func TestMediaConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{"disabled", Config{}, true},
		{"partial", Config{LiveKitURL: "wss://calls.example.com"}, false},
		{"local", Config{LiveKitURL: "ws://localhost:7880", LiveKitAPIKey: "devkey", LiveKitSecret: "secret"}, true},
		{"hosted", Config{SecureCookies: true, LiveKitURL: "wss://calls.example.com", LiveKitAPIKey: "key", LiveKitSecret: "secret"}, true},
		{"insecure hosted", Config{SecureCookies: true, LiveKitURL: "ws://calls.example.com", LiveKitAPIKey: "key", LiveKitSecret: "secret"}, false},
		{"invalid endpoint", Config{LiveKitURL: "https://calls.example.com", LiveKitAPIKey: "key", LiveKitSecret: "secret"}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if err := validateMediaConfig(item.cfg); (err == nil) != item.valid {
				t.Fatalf("unexpected config result: %v", err)
			}
		})
	}
}
