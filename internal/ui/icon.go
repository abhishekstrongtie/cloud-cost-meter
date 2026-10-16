package ui

import _ "embed"

// cloudTemplate is the menu bar icon: a black cloud on a transparent background. macOS tints
// "template" images itself, so it suits both light and dark menu bars.
//
//go:embed cloudTemplate.png
var cloudTemplate []byte
