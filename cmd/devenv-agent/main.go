// devenv-agent is the helper binary mounted into environments. See
// internal/agent for the protocol.
package main

import (
	"os"

	"github.com/tinyrack-net/devenv/internal/agent"
)

// version is set at build time.
var version = "dev"

func main() { os.Exit(agent.Main(os.Args[1:], version)) }
