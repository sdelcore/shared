package web

import _ "embed"

//go:embed shared.js
var SharedJS []byte

//go:embed home.html
var HomeHTML []byte

//go:embed init/index.html
var InitIndexHTML []byte

//go:embed init/SKILL.md
var InitSkillMD []byte
