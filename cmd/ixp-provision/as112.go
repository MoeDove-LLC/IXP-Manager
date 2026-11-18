package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/moedove/ixp-manager/internal/as112"
)

func runAS112(args []string, output io.Writer, probe bool) (int, error) {
	name := "as112-extract"
	if probe {
		name = "as112-probe"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	file := fs.String("file", "", "exact downloaded AS112 JSON bundle")
	want := fs.String("checksum", "", "actual bundle SHA-256, not the review token")
	router := fs.String("router", "", "expected AS112 router ID")
	destination := fs.String("output", "", "new private extraction directory; never overwrites an existing path")
	done, err := parse(fs, args)
	if err != nil {
		return 1, err
	}
	if done {
		return 0, nil
	}
	if *file == "" || !validChecksum(*want) || !identifier.MatchString(*router) {
		return 1, errors.New("--file, --checksum and --router are required")
	}
	if probe && *destination != "" || !probe && *destination == "" {
		return 1, errors.New("--output is required only for AS112 extraction")
	}
	raw, err := readRegular(*file, as112.MaxBundleBytes, false)
	if err != nil || checksum(string(raw)) != *want {
		return 1, errors.New("AS112 bundle does not match its actual checksum")
	}
	bundle, err := as112.Decode(string(raw))
	if err != nil || bundle.RouterID != *router {
		return 1, errors.New("AS112 bundle does not match the expected fixed-service/router contract")
	}
	if !probe {
		if err = as112.WriteNewDirectory(*destination, bundle); err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(output).Encode(map[string]any{"directory": *destination, "routerId": bundle.RouterID, "sha256": *want, "installed": false, "healthExecuted": false})
	}
	result, err := as112.Probe(context.Background(), bundle)
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return 3, encodeErr
	}
	if err != nil {
		return 3, err
	}
	if !result.Healthy {
		return 2, nil
	}
	return 0, nil
}
