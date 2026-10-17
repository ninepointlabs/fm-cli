// Package fmcli holds files from the repository root that the binary embeds.
package fmcli

import _ "embed"

// License is the text of the LICENSE file, shown in the About screen.
//
//go:embed LICENSE
var License string
