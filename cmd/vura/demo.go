package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/seed"
	"github.com/valicm/vura/internal/store"
)

// demoCmd builds a self-contained demo (config + database with one synthetic
// day) in a directory and prints how to look at it. Never touches the real
// config or data.
func demoCmd(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(os.TempDir(), "vura-demo"), "where to put the demo config and database")
	day := fs.String("day", time.Now().AddDate(0, 0, -1).Format("2006-01-02"), "the synthetic day (YYYY-MM-DD)")
	_ = fs.Parse(flagsFirst(fs, args))
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	cfgPath := filepath.Join(*dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(seed.Config), 0o600); err != nil {
		return err
	}
	os.Setenv("VURA_CONFIG", cfgPath)
	os.Setenv("VURA_DATA", *dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(cfg.DBPath + suffix)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	date, err := time.ParseInLocation("2006-01-02", *day, cfg.Location)
	if err != nil {
		return fmt.Errorf("--day: want YYYY-MM-DD")
	}
	if err := seed.Day(context.Background(), st, date, cfg.Location); err != nil {
		return err
	}
	fmt.Printf("demo written to %s\n\n", *dir)
	fmt.Printf("  export VURA_CONFIG=%s VURA_DATA=%s\n", cfgPath, *dir)
	fmt.Printf("  vura day %s\n", *day)
	fmt.Printf("  vura status\n")
	fmt.Printf("  vura reconcile %s --dry-run\n\n", *day)
	fmt.Println("unset both variables to return to your own data.")
	return nil
}
