package platform

import _ "embed"

// Tooling is built before the final executable layer. Updating a Daemon never
// rebuilds an existing container; this recipe is only used for new Computers.
//
//go:embed computer-image/Dockerfile
var computerDockerfile string
