// Package systemd embeds the service unit installed by `milo install`.
package systemd

import _ "embed"

// Unit is the contents of milo.service.
//
//go:embed milo.service
var Unit []byte
