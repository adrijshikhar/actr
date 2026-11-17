//go:build !darwin && !linux

package service

import (
	"errors"
)

var errUnsupportedOS = errors.New("unsupported operating system for service management")

// Install returns an error on unsupported platforms.
func Install(binPath string) error {
	return errUnsupportedOS
}

// Status returns an error on unsupported platforms.
func Status() (string, error) {
	return "", errUnsupportedOS
}

// Start returns an error on unsupported platforms.
func Start() error {
	return errUnsupportedOS
}

// Stop returns an error on unsupported platforms.
func Stop() error {
	return errUnsupportedOS
}

// Uninstall returns an error on unsupported platforms.
func Uninstall() error {
	return errUnsupportedOS
}
