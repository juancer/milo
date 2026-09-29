// Package systemd embeds the service unit installed by `nexo install`.
package systemd

import _ "embed"

// Unit is the contents of nexo.service.
//
//go:embed nexo.service
var Unit []byte
