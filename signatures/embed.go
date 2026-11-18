// Package signatures embeds the signature registry so the binary carries it.
// The registry itself is registry.yaml in this directory.
package signatures

import _ "embed"

// YAML is the content of registry.yaml as shipped with this build.
//
//go:embed registry.yaml
var YAML []byte
