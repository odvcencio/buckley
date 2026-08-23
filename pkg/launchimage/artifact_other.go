//go:build !linux

package launchimage

import "errors"

func openRootBinding(string) (*rootBinding, error) {
	return nil, errors.New("artifact root binding is unavailable")
}

func validateLaunchArtifactPath(string, uint32) error {
	return errors.New("artifact directory admission is unavailable")
}
