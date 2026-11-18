package action

import (
	"context"
	"os"
)

// LockFileForTest exposes internal lockFile for testing context timeouts.
func LockFileForTest(ctx context.Context, f *os.File) error {
	return lockFile(ctx, f)
}

// UnlockFileForTest exposes internal unlockFile for testing.
func UnlockFileForTest(f *os.File) error {
	return unlockFile(f)
}
