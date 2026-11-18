package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/policy"
)

const usage = `Usage:
 ixp-policy irr --server HOST:PORT --as-set AS-EXAMPLE --output irr.json [--sources RIPE,RADB] [--ttl 1h]
 ixp-policy roa-fetch --url https://validator.example/vrps.json --output rpki.json [--ttl 1h]
 ixp-policy roa-import --input vrps.json --source trusted-validator --output rpki.json [--ttl 1h]
 ixp-policy simulate --input request.json
 ixp-policy candidates --input irr.json --asn 64501
Collectors write review snapshots only. They never approve prefixes or apply router configuration.
ROA input must be a trusted validating cache export with generation metadata; this tool does not validate RPKI signatures.`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, usage)
		return nil
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	server := fs.String("server", "", "explicit trusted IRR WHOIS server host:port")
	asSet := fs.String("as-set", "", "AS-SET or AS<number> to expand")
	sources := fs.String("sources", "", "comma-separated trusted IRR registry sources")
	output := fs.String("output", "", "atomic private snapshot output file")
	input := fs.String("input", "", "input JSON file")
	endpoint := fs.String("url", "", "trusted validated ROA export URL")
	source := fs.String("source", "", "trusted validator identity for offline export")
	ttl := fs.Duration("ttl", time.Hour, "snapshot lifetime, maximum24h")
	includeOrigins := fs.Bool("include-as-set-origins", false, "explicitly review all prefix-origin pairs in the expanded AS-SET")
	asn := fs.Uint64("asn", 0, "candidate origin ASN")
	allowHTTP := fs.Bool("allow-http", false, "explicitly permit unencrypted HTTP for a trusted local cache")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var result any
	switch mode {
	case "irr":
		if *server == "" || *asSet == "" || *output == "" {
			return errors.New("--server, --as-set and --output are required")
		}
		registries := []string{}
		if *sources != "" {
			registries = strings.Split(*sources, ",")
		}
		snapshot, err := policy.CollectIRR(ctx, policy.WhoisClient{Address: *server, Sources: registries}, *asSet, *ttl)
		if err != nil {
			return err
		}
		result = snapshot
	case "roa-fetch":
		if *endpoint == "" || *output == "" {
			return errors.New("--url and --output are required")
		}
		snapshot, err := policy.FetchROAs(ctx, *endpoint, *ttl, *allowHTTP)
		if err != nil {
			return err
		}
		result = snapshot
	case "roa-import":
		if *input == "" || *source == "" || *output == "" {
			return errors.New("--input, --source and --output are required")
		}
		file, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer file.Close()
		snapshot, err := policy.ParseROAExport(file, *source, *ttl, time.Now().UTC())
		if err != nil {
			return err
		}
		result = snapshot
	case "simulate":
		if *input == "" {
			return errors.New("--input is required")
		}
		var request policy.Request
		if err := policy.ReadJSON(*input, &request); err != nil {
			return err
		}
		decisions, err := policy.Simulate(request, time.Now().UTC())
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(decisions)
	case "candidates":
		if *input == "" || *asn == 0 || *asn > 4294967294 {
			return errors.New("--input and a valid --asn are required")
		}
		var snapshot policy.IRRSnapshot
		if err := policy.ReadJSON(*input, &snapshot); err != nil {
			return err
		}
		routes, err := policy.CandidateRoutes(snapshot, uint32(*asn), *includeOrigins, time.Now().UTC())
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			Routes         []policy.Route `json:"routes"`
			ReviewRequired bool           `json:"reviewRequired"`
		}{routes, true})
	default:
		return errors.New(usage)
	}
	if err := policy.WriteSnapshot(*output, result); err != nil {
		return err
	}
	fmt.Fprintln(out, "Policy snapshot saved; explicit review is required before changing any allowlist.")
	return nil
}
