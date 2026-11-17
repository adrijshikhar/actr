package registration

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var ErrHeadlessEnvironment = errors.New("headless environment: no display server available")

func validateBrowserURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("target URL is empty")
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid browser target URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid browser target URL: scheme must be http or https, got %q", parsed.Scheme)
	}
	return trimmed, nil
}

func browserCommand(goos, rawURL string) (*exec.Cmd, error) {
	switch goos {
	case "darwin":
		return exec.Command("open", rawURL), nil
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil, ErrHeadlessEnvironment
		}
		return exec.Command("xdg-open", rawURL), nil
	case "windows":
		return exec.Command("cmd.exe", "/c", "start", "", rawURL), nil
	default:
		return nil, fmt.Errorf("unsupported platform for browser launch: %s", goos)
	}
}

func defaultOSBrowserLauncher(targetURL string) error {
	validatedURL, err := validateBrowserURL(targetURL)
	if err != nil {
		return err
	}

	cmd, err := browserCommand(runtime.GOOS, validatedURL)
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

// OpenBrowser attempts to open targetURL in the system's default browser.
// If browserLauncher is nil, defaultOSBrowserLauncher is used.
func OpenBrowser(targetURL string, browserLauncher func(url string) error) error {
	validatedURL, err := validateBrowserURL(targetURL)
	if err != nil {
		return err
	}

	if browserLauncher == nil {
		browserLauncher = defaultOSBrowserLauncher
	}
	return browserLauncher(validatedURL)
}
