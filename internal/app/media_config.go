package app

import (
	"fmt"
	"net/url"
	"strings"
)

func validateMediaConfig(cfg Config) error {
	values := []string{cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitSecret}
	configured := 0
	for _, value := range values {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("LIVEKIT configuration values must not contain surrounding whitespace")
		}
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != 3 {
		return fmt.Errorf("configure all three LIVEKIT_URL, LIVEKIT_API_KEY, and LIVEKIT_API_SECRET values, or leave calls disabled")
	}
	endpoint, err := url.Parse(cfg.LiveKitURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
		return fmt.Errorf("LIVEKIT_URL must be a ws:// or wss:// endpoint without credentials, query, or fragment")
	}
	if cfg.SecureCookies && endpoint.Scheme != "wss" {
		return fmt.Errorf("hosted calls require a secure wss:// LIVEKIT_URL")
	}
	return nil
}
