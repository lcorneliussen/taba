// Package openclawplugin is the OpenClaw plugin, embedded in wtc so that
// `wtc openclaw install` installs the plugin of the same version.
package openclawplugin

import "embed"

//go:embed index.js sandbox-git.js publication.js package.json openclaw.plugin.json dist skills
var Files embed.FS
