package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateDarwinPlist generates a launchd property list XML string for macOS.
func GenerateDarwinPlist(binPath, workDir string) string {
	home, _ := os.UserHomeDir()
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	standardPaths := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
	}
	if home != "" {
		standardPaths = append(standardPaths,
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".local", "bin"),
		)
	}
	for _, p := range standardPaths {
		if !strings.Contains(":"+pathEnv+":", ":"+p+":") {
			pathEnv = p + ":" + pathEnv
		}
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.github.actr</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
        <string>--config-dir</string>
        <string>%s</string>
    </array>
    <key>WorkingDirectory</key>
    <string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>%s</string>
        <key>HOME</key>
        <string>%s</string>
    </dict>
    <key>StandardOutPath</key>
    <string>%s/daemon.log</string>
    <key>StandardErrorPath</key>
    <string>%s/daemon.log</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>`, binPath, workDir, workDir, pathEnv, home, workDir, workDir)
}

// GenerateLinuxUnit generates a systemd service unit string for Linux.
func GenerateLinuxUnit(binPath, workDir string) string {
	home, _ := os.UserHomeDir()
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	return fmt.Sprintf(`[Unit]
Description=actr GitHub Actions Runner
After=network.target

[Service]
ExecStart=%s run --config-dir %s
WorkingDirectory=%s
Environment="PATH=%s"
Environment="HOME=%s"
StandardOutput=append:%s/daemon.log
StandardError=append:%s/daemon.log
Restart=always
RestartSec=5

[Install]
WantedBy=default.target`, binPath, workDir, workDir, pathEnv, home, workDir, workDir)
}

func resolveBinPath(binPath string) (string, error) {
	if binPath != "" {
		return filepath.Abs(binPath)
	}
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return filepath.Abs(execPath)
}
