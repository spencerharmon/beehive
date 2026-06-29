// Command beehived is the frontend daemon, the only long-running process.
// P0 is a stub; P3 ships the htmx views over the repo model.
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
	fmt.Printf("beehived stub; config dir %s, agent %s\n", c.Dir, c.AgentCmd)
}
