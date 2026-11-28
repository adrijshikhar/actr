//go:build !darwin

package power

type noopInhibitor struct{}

func newPlatformInhibitor() inhibitor {
	return &noopInhibitor{}
}

func (n *noopInhibitor) Acquire() error { return nil }
func (n *noopInhibitor) Release() error { return nil }
