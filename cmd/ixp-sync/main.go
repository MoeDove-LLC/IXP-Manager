// ixp-sync fetches a public PeeringDB snapshot, pins its bytes, and optionally
// submits a review proposal. Its explicit mailing-* subcommands additionally
// preview and apply narrowly scoped Mailman membership intentions.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/integrations"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "mailing-") {
		return mailing(args, output)
	}
	fs := flag.NewFlagSet("ixp-sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("provider-config", "", "private allowlisted provider JSON")
	asn := fs.Uint64("asn", 0, "network ASN")
	ix := fs.Int64("ix", 0, "optional PeeringDB exchange ID")
	facility := fs.Int64("facility", 0, "optional PeeringDB facility ID")
	path := fs.String("out", "", "new snapshot JSON output path")
	endpoint := fs.String("submit", "", "HTTPS manager /api/integrations/proposals/import URL")
	memberID := fs.String("member-id", "", "local member record ID for review proposal")
	connectionID := fs.String("connection-id", "", "optional local connection ID")
	tokenFile := fs.String("token-file", "", "private integrations:write token file")
	tokenStdin := fs.Bool("token-stdin", false, "read integration token from stdin")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid sync flags; credentials must only come from stdin or private file")
	}
	if fs.NArg() != 0 || *configPath == "" || *path == "" || *asn == 0 || *asn > 4294967294 {
		return errors.New("--provider-config, --asn and --out are required")
	}
	var credential string
	if *endpoint != "" {
		if *memberID == "" || (*tokenFile == "") == (!*tokenStdin) {
			return errors.New("submission requires --member-id and exactly one of --token-file/--token-stdin")
		}
		if err := validateSubmitURL(*endpoint); err != nil {
			return err
		}
		var raw []byte
		var err error
		if *tokenFile != "" {
			info, e := os.Lstat(*tokenFile)
			if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
				return errors.New("token file must be private (0600) and at most 256 bytes")
			}
			raw, err = os.ReadFile(*tokenFile)
		} else {
			raw, err = io.ReadAll(io.LimitReader(input, 257))
		}
		if err != nil {
			return errors.New("could not read integration token")
		}
		credential = strings.TrimSpace(string(raw))
		if credential == "" || len(credential) > 256 || strings.ContainsAny(credential, "\r\n") {
			return errors.New("invalid integration token")
		}
	} else if *tokenFile != "" || *tokenStdin {
		return errors.New("token input is only used with --submit")
	}
	config, err := integrations.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	client, err := integrations.NewClient(config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snapshot, err := client.Fetch(ctx, uint32(*asn), *ix, *facility)
	if err != nil {
		return err
	}
	document, checksum, err := integrations.Canonical(snapshot)
	if err != nil {
		return err
	}
	if err = writeSnapshot(*path, document, checksum); err != nil {
		return err
	}
	fmt.Fprintf(output, "Snapshot written to %s\nSHA-256: %s\n", *path, checksum)
	if *endpoint != "" {
		if len(document) > 6<<20 {
			return errors.New("snapshot exceeds manager's 6 MiB offline proposal import limit")
		}
		id, err := submit(ctx, &http.Client{Timeout: 20 * time.Second}, *endpoint, credential, *memberID, *connectionID, string(document), checksum)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Review proposal created: %s (not applied)\n", id)
	}
	return nil
}
func writeSnapshot(path string, raw []byte, checksum string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("snapshot output must be a new writable file")
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	hashFile, err := os.OpenFile(path+".sha256", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("checksum output already exists or cannot be written")
	}
	if _, err = io.WriteString(hashFile, checksum+"\n"); err != nil {
		hashFile.Close()
		os.Remove(path + ".sha256")
		return err
	}
	if err = hashFile.Sync(); err != nil {
		hashFile.Close()
		os.Remove(path + ".sha256")
		return err
	}
	if err = hashFile.Close(); err != nil {
		return err
	}
	success = true
	return nil
}
func validateSubmitURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/integrations/proposals/import" {
		return errors.New("submission URL must be HTTPS /api/integrations/proposals/import without credentials/query")
	}
	return nil
}
func submit(ctx context.Context, client *http.Client, endpoint, credential, memberID, connectionID, document, checksum string) (string, error) {
	if err := validateSubmitURL(endpoint); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(map[string]any{"memberId": memberID, "connectionId": connectionID, "document": document, "checksum": checksum})
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safe.Do(request)
	if err != nil {
		return "", errors.New("manager submission failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		return "", fmt.Errorf("manager rejected submission with HTTP %d", response.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 10<<20))
	if err = decoder.Decode(&result); err != nil || result.ID == "" {
		return "", errors.New("manager response did not contain proposal ID")
	}
	return result.ID, nil
}
