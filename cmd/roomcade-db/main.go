package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"roomcade/internal/app"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: roomcade-db backup -source DATABASE -output NEW_FILE | verify -source DATABASE")
		os.Exit(2)
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ExitOnError)
	source := flags.String("source", "", "existing SQLite database")
	output := flags.String("output", "", "new backup file (backup only)")
	flags.Parse(os.Args[2:])
	if *source == "" || flags.NArg() != 0 || (command != "backup" && command != "verify") || (command == "backup" && *output == "") || (command == "verify" && *output != "") {
		flags.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var err error
	if command == "backup" {
		err = app.BackupDatabase(ctx, *source, *output)
	} else {
		err = app.VerifyDatabase(ctx, *source)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "database operation failed:", err)
		os.Exit(1)
	}
	fmt.Println(command, "verified")
}
