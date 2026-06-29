// Command honeybee runs one autonomous agent on one task. P0 is a stub; P2 ships
// the opencode session turn loop, deterministic completion checks, and GC.
package main

import (
	"fmt"

	"github.com/spencerharmon/beehive/internal/config"
)

func main() {
	c, err := config.Load()
	if err != nil {
		fmt.Println("config error:", err)
		return
	}
	fmt.Printf("honeybee stub; ttl=%dm turns=%d agent=%s\n", c.TTLMinutes, c.MaxTurns, c.AgentCmd)
}
