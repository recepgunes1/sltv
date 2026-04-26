// Command sctl is the SLTV CLI. It talks to sltvd over gRPC and
// drives all disk and attachment operations from the shell.
package main

import (
	"fmt"
	"os"

	"github.com/sltv/sltv/internal/cli"
)

func main() {
	if err := cli.New().Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
