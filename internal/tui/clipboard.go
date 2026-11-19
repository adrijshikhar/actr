package tui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var (
	clipboardWriter io.Writer = os.Stdout
	execCommand               = exec.Command
	currentGOOS               = runtime.GOOS
)

// CopyText copies text to the system clipboard using OSC 52 escape sequences
// with a fallback to macOS pbcopy when running on darwin. Missing tools or
// execution errors are handled gracefully without panicking or failing.
func CopyText(text string) error {
	// 1. Emit OSC 52 escape sequence: \x1b]52;c;<base64>\x07
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	osc52 := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
	if clipboardWriter != nil {
		_, _ = fmt.Fprint(clipboardWriter, osc52)
	}

	// 2. On macOS (darwin), pipe to pbcopy as a native fallback
	if currentGOOS == "darwin" {
		cmd := execCommand("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	}

	return nil
}

// ClipboardCopy is an alias for CopyText.
func ClipboardCopy(text string) error {
	return CopyText(text)
}
