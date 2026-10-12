// AgentRelay-worker is the standalone target executable. It imports no
// Controller, MCP server, SSH executor, local source scanner or agent runtime.
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if err := workerCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
