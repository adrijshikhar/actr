package registration

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var ErrHeadlessEnvironment = errors.New("headless environment: no display server available")

func defaultOSBrowserLauncher(targetURL string) error {
	trimmed := strings.TrimSpace(targetURL)
	if trimmed == "" {
		return fmt.Errorf("target URL is empty")
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return fmt.Errorf("invalid browser target URL: must be http or https scheme")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", trimmed)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return ErrHeadlessEnvironment
		}
		cmd = exec.Command("xdg-open", trimmed)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", trimmed)
	default:
		return fmt.Errorf("unsupported platform for browser launch: %s", runtime.GOOS)
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
	if browserLauncher == nil {
		browserLauncher = defaultOSBrowserLauncher
	}
	return browserLauncher(targetURL)
}
