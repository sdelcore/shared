package web

import _ "embed"

//go:embed shared.js
var SharedJS []byte

//go:embed home.html
var HomeHTML []byte
