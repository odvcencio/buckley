//go:build !linux

package workspaceguard

func OpenRootBinding(string) (*RootBinding, error) {
	return nil, ErrRootBindingUnavailable
}
