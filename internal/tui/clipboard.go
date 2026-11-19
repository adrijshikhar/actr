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

// Clipboard defines the capability to copy text to the system clipboard.
type Clipboard interface {
	Copy(text string) error
}

// SystemClipboard implements Clipboard using OSC 52 with fallback to pbcopy.
type SystemClipboard struct{}

// Copy sends text to the system clipboard.
func (s SystemClipboard) Copy(text string) error {
	return CopyText(text)
}

// DefaultClipboard is the default Clipboard implementation.
var DefaultClipboard Clipboard = SystemClipboard{}

var (
	clipboardWriter io.Writer = os.Stdout
	execCommand               = exec.Command
	currentGOOS               = runtime.GOOS
)

// CopyText copies text to the system clipboard using OSC 52 escape sequences
// with a fallback to macOS pbcopy when running on darwin.
func CopyText(text string) error {
	var oscErr error
	if clipboardWriter != nil {
		b64 := base64.StdEncoding.EncodeToString([]byte(text))
		osc52 := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
		_, oscErr = fmt.Fprint(clipboardWriter, osc52)
	}

	if currentGOOS == "darwin" {
		cmd := execCommand("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		if pbErr := cmd.Run(); pbErr != nil {
			if oscErr != nil {
				return fmt.Errorf("copy to clipboard failed: pbcopy: %w; osc52: %v", pbErr, oscErr)
			}
			return pbErr
		}
		return nil
	}

	return oscErr
}

// ClipboardCopy is an alias for CopyText.
func ClipboardCopy(text string) error {
	return CopyText(text)
}

// SetClipboardWriterForTest allows unit tests to stub clipboardWriter.
func SetClipboardWriterForTest(w io.Writer) func() {
	orig := clipboardWriter
	clipboardWriter = w
	return func() { clipboardWriter = orig }
}

// SetExecCommandForTest allows unit tests to stub execCommand.
func SetExecCommandForTest(fn func(name string, arg ...string) *exec.Cmd) func() {
	orig := execCommand
	execCommand = fn
	return func() { execCommand = orig }
}
