package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/moedove/ixp-manager/internal/backup"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 || (args[0] != "create" && args[0] != "restore" && args[0] != "verify") {
		return fmt.Errorf("usage: ixp-backup create --data DIR --output FILE.tar.gz | verify --input FILE.tar.gz [--work-dir DIR] | restore --input FILE.tar.gz --data NEW_DIR")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	data := flags.String("data", "", "data directory (restore requires a new path)")
	input := flags.String("input", "", "archive to restore")
	output := flags.String("output", "", "new private archive")
	workDir := flags.String("work-dir", "", "existing parent for a temporary verification copy (verify only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("positional arguments are not supported")
	}
	if args[0] == "verify" {
		if *input == "" || *data != "" || *output != "" {
			return fmt.Errorf("verify requires --input and accepts an optional --work-dir")
		}
		if err := backup.Verify(*input, *workDir); err != nil {
			return err
		}
		fmt.Println("Backup hashes and staged state verified by this version. Verification files removed.")
		return nil
	}
	if *data == "" || *workDir != "" {
		return fmt.Errorf("create and restore require --data; --work-dir is for verify only")
	}
	if args[0] == "create" {
		if *input != "" || *output == "" {
			return fmt.Errorf("create requires --output only")
		}
		if err := backup.Create(*data, *output); err != nil {
			return err
		}
		fmt.Println("Private backup created; store it securely with restricted access.")
		return nil
	}
	if *output != "" || *input == "" {
		return fmt.Errorf("restore requires --input only")
	}
	if err := backup.Restore(*input, *data); err != nil {
		return err
	}
	fmt.Println("Backup hashes and restored state verified; new data directory is ready.")
	return nil
}
