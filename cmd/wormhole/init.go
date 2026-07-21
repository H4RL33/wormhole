package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/H4RL33/wormhole/internal/config"
	"golang.org/x/term"
)

// runInit implements wormhole init: interactive setup wizard to create .wormhole/config.toml
func runInit(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "wormhole init: takes no arguments\n")
		return 2
	}

	// Check if stdin is a TTY
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(stderr, "wormhole init: stdin is not a TTY (run from an interactive terminal)\n")
		return 1
	}

	// Determine local config path: current directory's .wormhole/config.toml
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole init: get current directory: %v\n", err)
		return 1
	}
	configPath := filepath.Join(cwd, ".wormhole", "config.toml")

	reader := bufio.NewReader(os.Stdin)

	// Guard against clobbering an existing config
	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(stdout, "%s already exists. Overwrite? [y/N]: ", configPath)
		confirm, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
			fmt.Fprintf(stdout, "Aborted.\n")
			return 1
		}
	}

	// Welcome
	fmt.Fprintf(stdout, "wormhole: interactive setup wizard\n\n")
	fmt.Fprintf(stdout, "Configure your Wormhole credentials in .wormhole/config.toml\n")
	fmt.Fprintf(stdout, "(leave blank to skip a field)\n\n")

	// Prompt for server
	fmt.Fprintf(stdout, "Wormhole server URL []: ")
	serverInput, _ := reader.ReadString('\n')
	server := strings.TrimSpace(serverInput)

	// Prompt for project
	fmt.Fprintf(stdout, "Project ID []: ")
	projectInput, _ := reader.ReadString('\n')
	project := strings.TrimSpace(projectInput)

	// Prompt for role
	fmt.Fprintf(stdout, "Role template [backend-engineer]: ")
	roleInput, _ := reader.ReadString('\n')
	role := strings.TrimSpace(roleInput)
	if role == "" {
		role = "backend-engineer"
	}

	// Create config
	cfg := config.Config{
		Server:  server,
		Project: project,
		Role:    role,
	}

	// Save config
	if err := cfg.Save(configPath); err != nil {
		fmt.Fprintf(stderr, "wormhole init: save config: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "\nConfiguration saved to %s\n", configPath)
	fmt.Fprintf(stdout, "Run 'wormhole join' or 'wormhole connect' to begin\n")
	return 0
}
