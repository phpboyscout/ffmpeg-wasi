//go:build !linux

package conformance

import "errors"

func landlockABI() (int, error) {
	return 0, errors.ErrUnsupported
}
