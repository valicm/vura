package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/store"
)

// backupCmd copies the database into <data>/backups/vura-YYYY-MM-DD.db and
// keeps the newest N. Run nightly by vura-backup.timer.
func backupCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(cfg.DataDir, "backups"), "backup directory")
	keep := fs.Int("keep", 30, "how many daily backups to keep")
	_ = fs.Parse(flagsFirst(fs, args))
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	name := "vura-" + time.Now().In(cfg.Location).Format("2006-01-02") + ".db"
	dst := filepath.Join(*dir, name)
	if err := st.Backup(context.Background(), dst); err != nil {
		return err
	}
	fi, _ := os.Stat(dst)
	fmt.Printf("wrote %s (%d KB)\n", dst, fi.Size()/1024)
	matches, _ := filepath.Glob(filepath.Join(*dir, "vura-*.db"))
	sort.Strings(matches)
	for len(matches) > *keep {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
	return nil
}
