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
}
