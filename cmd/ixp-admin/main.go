// ixp-admin performs deliberate offline account recovery, never accepting
// passwords in command arguments or environment variables.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moedove/ixp-manager/internal/app"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 || args[0] != "reset-password" {
		return errors.New("usage: ixp-admin reset-password --data-dir DIR --email ACCOUNT --password-stdin [--disable-mfa]")
	}
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("data-dir", "", "existing private application data directory")
	email := fs.String("email", "", "account email")
	passwordStdin := fs.Bool("password-stdin", false, "read new password from stdin")
	disable := fs.Bool("disable-mfa", false, "explicitly remove MFA and recovery codes")
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New("invalid recovery flags; passwords must only be supplied through stdin")
	}
	if fs.NArg() != 0 || *dir == "" || *email == "" || !*passwordStdin {
		return errors.New("--data-dir, --email and --password-stdin are required; no positional password is accepted")
	}
	raw, err := io.ReadAll(io.LimitReader(input, 131))
	if err != nil {
		return errors.New("could not read password from stdin")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(password) < 12 || len(password) > 128 || strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("stdin must contain one password of 12 to 128 bytes")
	}
	if err = app.ResetPassword(*dir, *email, password, *disable); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Password reset completed; API tokens and sessions revoked. MFA preserved unless --disable-mfa was supplied.")
	return err
}
