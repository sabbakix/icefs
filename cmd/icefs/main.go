// Command icefs is the IceFs terminal interface. It talks to icefsd over its
// root-only unix socket, so run it as root (or with sudo).
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sabbakix/icefs/internal/client"
	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/tui"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to config.toml (used to find the socket)")
	socket := flag.String("socket", "", "path to the icefsd socket (overrides the config)")
	flag.Parse()

	sock := *socket
	if sock == "" {
		sock = config.DefaultSocketPath
		if cfg, err := config.Load(*cfgPath); err == nil {
			sock = cfg.SocketPath
		}
	}
	p := tea.NewProgram(tui.New(client.New(sock)), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "icefs:", err)
		os.Exit(1)
	}
}
