package main

import (
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/launch"
)

// openCmd opens the dashboard as an app window. The daemon is the server.
func openCmd(cfg *config.Config) error {
	return launch.Open("http://"+cfg.Sources.Listen+"/", cfg.DataDir)
}
