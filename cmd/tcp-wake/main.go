// Command tcp-wake is a wake-on-demand reverse proxy that holds requests to a
// powered-off host and forwards them once it is healthy.
package main

import (
	"fmt"
	"os"

	"tcp-wake/internal/config"
)

func main() {
	if _, err := config.Load(os.Args[1:], os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
