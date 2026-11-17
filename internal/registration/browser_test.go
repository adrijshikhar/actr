package registration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenBrowser(t *testing.T) {
	t.Run("success with custom launcher", func(t *testing.T) {
		var calledURL string
		mockLauncher := func(url string) error {
			calledURL = url
			return nil
		}

		err := OpenBrowser("https://github.com/test", mockLauncher)
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/test", calledURL)
	})

	t.Run("launcher error", func(t *testing.T) {
		mockLauncher := func(url string) error {
			return errors.New("failed to launch browser")
		}

		err := OpenBrowser("https://github.com/test", mockLauncher)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to launch browser")
	})

	t.Run("headless error", func(t *testing.T) {
		mockLauncher := func(url string) error {
			return ErrHeadlessEnvironment
		}

		err := OpenBrowser("https://github.com/test", mockLauncher)
		require.ErrorIs(t, err, ErrHeadlessEnvironment)
	})

	t.Run("invalid urls rejected", func(t *testing.T) {
		called := false
		mockLauncher := func(url string) error {
			called = true
			return nil
		}

		invalidCases := []string{
			"",
			"   ",
			"ftp://example.com",
			"file:///etc/passwd",
			"javascript:alert(1)",
			"custom-scheme://test",
			"not a url at all",
			"://missing-scheme",
			`https://github.com/test"quote`,
			`https://github.com/test'quote`,
			"https://github.com/test`backtick`",
			"https://github.com/test with spaces",
			"https://github.com/test&param=1",
			"https://github.com/test^caret",
			"https://github.com/test<bracket",
			"https://github.com/test>bracket",
			"https://github.com/test|pipe",
		}

		for _, tc := range invalidCases {
			t.Run(tc, func(t *testing.T) {
				called = false
				err := OpenBrowser(tc, mockLauncher)
				require.Error(t, err)
				assert.False(t, called, "launcher should not be called for invalid url %q", tc)
			})
		}
	})

	t.Run("urls with illegal characters rejected", func(t *testing.T) {
		illegalCases := []struct {
			name string
			url  string
		}{
			{"double quote", `https://github.com/test"inject`},
			{"single quote", `https://github.com/test'inject`},
			{"backtick", "https://github.com/test`inject`"},
			{"space", "https://github.com/test space"},
			{"ampersand", "https://github.com/test&foo=bar"},
			{"caret", "https://github.com/test^car"},
			{"pipe", "https://github.com/test|pipe"},
			{"less than", "https://github.com/test<tag"},
			{"greater than", "https://github.com/test>tag"},
			{"newline", "https://github.com/test\ninject"},
			{"carriage return", "https://github.com/test\rinject"},
			{"tab", "https://github.com/test\tinject"},
		}

		for _, tc := range illegalCases {
			t.Run(tc.name, func(t *testing.T) {
				err := OpenBrowser(tc.url, nil)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "contains illegal characters")
			})
		}
	})

	t.Run("valid http and https accepted", func(t *testing.T) {
		var received string
		mockLauncher := func(url string) error {
			received = url
			return nil
		}

		validCases := []string{
			"http://localhost:8080",
			"https://github.com/adrijshikhar/actr",
			"https://github.com/settings/runners/new?repo=foo",
		}

		for _, tc := range validCases {
			t.Run(tc, func(t *testing.T) {
				err := OpenBrowser(tc, mockLauncher)
				require.NoError(t, err)
				assert.Equal(t, tc, received)
			})
		}
	})
}

func TestBrowserCommand(t *testing.T) {
	testURL := "https://github.com/adrijshikhar/actr"

	t.Run("windows command structure", func(t *testing.T) {
		cmd, err := browserCommand("windows", testURL)
		require.NoError(t, err)
		assert.Equal(t, []string{"rundll32", "url.dll,FileProtocolHandler", testURL}, cmd.Args)
	})

	t.Run("darwin command structure", func(t *testing.T) {
		cmd, err := browserCommand("darwin", testURL)
		require.NoError(t, err)
		assert.Equal(t, []string{"open", testURL}, cmd.Args)
	})

	t.Run("linux command structure with display", func(t *testing.T) {
		t.Setenv("DISPLAY", ":0")
		cmd, err := browserCommand("linux", testURL)
		require.NoError(t, err)
		assert.Equal(t, []string{"xdg-open", testURL}, cmd.Args)
	})

	t.Run("linux headless without display", func(t *testing.T) {
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		cmd, err := browserCommand("linux", testURL)
		require.ErrorIs(t, err, ErrHeadlessEnvironment)
		assert.Nil(t, cmd)
	})

	t.Run("unsupported platform", func(t *testing.T) {
		cmd, err := browserCommand("plan9", testURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported platform")
		assert.Nil(t, cmd)
	})
}
