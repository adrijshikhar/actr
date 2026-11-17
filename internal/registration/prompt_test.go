package registration

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrompt(t *testing.T) {
	t.Run("accepts user input", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("https://github.com/custom/repo\n"))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter repo URL", "https://github.com/default/repo")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/custom/repo", val)
		assert.Equal(t, "Enter repo URL [https://github.com/default/repo]: ", out.String())
	})

	t.Run("uses default on empty enter", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n"))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter repo URL", "https://github.com/default/repo")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/default/repo", val)
		assert.Equal(t, "Enter repo URL [https://github.com/default/repo]: ", out.String())
	})

	t.Run("no default value formatting", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("my-runner\n"))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter runner name", "")
		require.NoError(t, err)
		assert.Equal(t, "my-runner", val)
		assert.Equal(t, "Enter runner name: ", out.String())
	})

	t.Run("empty input with no default", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n"))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter runner name", "")
		require.NoError(t, err)
		assert.Equal(t, "", val)
		assert.Equal(t, "Enter runner name: ", out.String())
	})

	t.Run("handles EOF gracefully", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("trailing-token-without-newline"))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter token", "default-token")
		require.NoError(t, err)
		assert.Equal(t, "trailing-token-without-newline", val)
	})

	t.Run("handles EOF on empty input", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader(""))
		var out bytes.Buffer

		val, err := Prompt(in, &out, "Enter token", "default-token")
		require.NoError(t, err)
		assert.Equal(t, "default-token", val)
	})

	t.Run("reader error other than EOF", func(t *testing.T) {
		errReader := &failingReader{err: io.ErrUnexpectedEOF}
		in := bufio.NewReader(errReader)
		var out bytes.Buffer

		_, err := Prompt(in, &out, "Enter token", "default-token")
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

func TestPromptConfirm(t *testing.T) {
	t.Run("default yes on Enter", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n"))
		var out bytes.Buffer

		ok, err := PromptConfirm(in, &out, "Continue?", true)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "Continue? [Y/n]: ", out.String())
	})

	t.Run("default no on Enter", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n"))
		var out bytes.Buffer

		ok, err := PromptConfirm(in, &out, "Continue?", false)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, "Continue? [y/N]: ", out.String())
	})

	t.Run("explicit no", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("n\n"))
		var out bytes.Buffer

		ok, err := PromptConfirm(in, &out, "Continue?", true)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("explicit yes variants", func(t *testing.T) {
		for _, ans := range []string{"y\n", "Y\n", "yes\n", "YES\n", "  Yes  \n"} {
			in := bufio.NewReader(strings.NewReader(ans))
			var out bytes.Buffer

			ok, err := PromptConfirm(in, &out, "Continue?", false)
			require.NoError(t, err)
			assert.True(t, ok, "expected true for %q", ans)
		}
	})

	t.Run("arbitrary text evaluates to false", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("invalid\n"))
		var out bytes.Buffer

		ok, err := PromptConfirm(in, &out, "Continue?", true)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("reader error other than EOF", func(t *testing.T) {
		errReader := &failingReader{err: io.ErrClosedPipe}
		in := bufio.NewReader(errReader)
		var out bytes.Buffer

		val, err := PromptConfirm(in, &out, "Continue?", true)
		assert.ErrorIs(t, err, io.ErrClosedPipe)
		assert.False(t, val)
	})
}

func TestIsTerminal(t *testing.T) {
	// An invalid file descriptor should return false
	assert.False(t, IsTerminal(^uintptr(0)))
}

func TestPromptPassword(t *testing.T) {
	t.Run("non-terminal falls back to reader", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("secret-token-123\n"))
		var out bytes.Buffer

		val, err := PromptPassword(in, &out, "Enter token")
		require.NoError(t, err)
		assert.Equal(t, "secret-token-123", val)
		assert.Equal(t, "Enter token: ", out.String())
	})

	t.Run("simulated terminal uses readPasswordFunc", func(t *testing.T) {
		orig := readPasswordFunc
		t.Cleanup(func() { readPasswordFunc = orig })

		readPasswordFunc = func(fd int) ([]byte, error) {
			return []byte("term-secret-token"), nil
		}

		in := bufio.NewReader(strings.NewReader("ignored\n"))
		var out bytes.Buffer

		// When os.Stdin is not a terminal in tests, PromptPassword uses the fallback reader
		val, err := PromptPassword(in, &out, "Enter token")
		require.NoError(t, err)
		assert.NotEmpty(t, val)
	})
}

type failingReader struct {
	err error
}

func (f *failingReader) Read(p []byte) (n int, err error) {
	return 0, f.err
}
