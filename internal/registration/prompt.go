package registration

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

var readPasswordFunc = term.ReadPassword

// IsTerminal returns true if the given file descriptor is attached to a terminal.
func IsTerminal(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}

// Prompt displays a prompt message with an optional default value and reads user input.
// If the user enters empty input (e.g. presses Enter), defaultValue is returned.
func Prompt(reader *bufio.Reader, writer io.Writer, promptText, defaultValue string) (string, error) {
	var err error
	if defaultValue != "" {
		_, err = fmt.Fprintf(writer, "%s [%s]: ", promptText, defaultValue)
	} else {
		_, err = fmt.Fprintf(writer, "%s: ", promptText)
	}
	if err != nil {
		return "", err
	}

	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return defaultValue, nil
	}
	return trimmed, nil
}

// PromptConfirm displays a confirmation prompt (yes/no) and returns true for yes and false for no.
// If the user enters empty input, defaultYes is returned. If input is closed with no answer, an error wrapping io.EOF is returned.
func PromptConfirm(reader *bufio.Reader, writer io.Writer, promptText string, defaultYes bool) (bool, error) {
	var suffix string
	if defaultYes {
		suffix = "[Y/n]"
	} else {
		suffix = "[y/N]"
	}

	if _, err := fmt.Fprintf(writer, "%s %s: ", promptText, suffix); err != nil {
		return false, err
	}
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}

	trimmed := strings.TrimSpace(line)
	if err == io.EOF && trimmed == "" {
		return false, fmt.Errorf("no answer provided: input closed: %w", io.EOF)
	}
	if trimmed == "" {
		return defaultYes, nil
	}

	ans := strings.ToLower(trimmed)
	if ans == "y" || ans == "yes" {
		return true, nil
	}
	return false, nil
}

// PromptPassword displays a prompt message and reads sensitive input (e.g. registration token)
// without echoing to the terminal. When attached to a terminal and reader is nil,
// term.ReadPassword is used; otherwise, it falls back to reading from reader.
func PromptPassword(reader *bufio.Reader, writer io.Writer, promptText string) (string, error) {
	if _, err := fmt.Fprintf(writer, "%s: ", promptText); err != nil {
		return "", err
	}
	if reader == nil && IsTerminal(os.Stdin.Fd()) {
		passBytes, err := readPasswordFunc(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(writer)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(passBytes)), nil
	}
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
