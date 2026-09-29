//go:build !linux

package gpio

import "errors"

// OpenChip is only available on Linux.
func OpenChip(string) (Controller, error) { return nil, errors.New("gpio: real GPIO requires Linux") }
