// Package version carries build identity.
package version

// Version is the release. Set by the linker in release builds:
//
//	go build -ldflags "-X .../internal/version.Version=$(git describe --tags)"
var Version = "0.1.0-dev"

// Name is the binary and MCP server name.
const Name = "toolgate"
