package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/1k-off/reksicherung/internal/reksicherung/catalog"
	"github.com/1k-off/reksicherung/internal/reksicherung/config"
)

const maxConfigBytes = 1 << 20

func runValidate(args []string, stdout, stderr io.Writer) error {
	options, err := parseConfigFlags("validate", args, stderr)
	if err != nil {
		return err
	}
	decoded, err := loadCatalog(options.path)
	if err != nil {
		return err
	}
	if options.production {
		if err := decoded.ValidateProduction(); err != nil {
			return fmt.Errorf("invalid production catalog: %w", err)
		}
	}
	_, err = fmt.Fprintf(stdout, "valid catalog: %d backup source(s)\n", len(decoded.Backups))
	return err
}

func runCatalog(args []string, stdout, stderr io.Writer) error {
	options, err := parseConfigFlags("catalog", args, stderr)
	if err != nil {
		return err
	}
	decoded, err := loadCatalog(options.path)
	if err != nil {
		return err
	}
	if options.production {
		if err := decoded.ValidateProduction(); err != nil {
			return fmt.Errorf("invalid production catalog: %w", err)
		}
	}
	sourceRef := options.sourceRef
	if sourceRef == "" {
		sourceRef = filepath.Base(options.path)
	}
	sourceRef = filepath.ToSlash(sourceRef)
	inventory, err := catalog.FromConfig(decoded, sourceRef)
	if err != nil {
		return err
	}
	output, err := inventory.Marshal()
	if err != nil {
		return err
	}
	if _, err := stdout.Write(output); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	if _, err := io.WriteString(stdout, "\n"); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	return nil
}

type cliOptions struct {
	path       string
	production bool
	sourceRef  string
}

func parseConfigFlags(command string, args []string, stderr io.Writer) (cliOptions, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "path to the Reksicherung YAML catalog")
	production := flags.Bool("production", false, "apply production safety validation, including image digest pinning")
	sourceRef := flags.String("source-ref", "", "stable relative catalog reference for inventory traceability")
	if err := flags.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if flags.NArg() != 0 {
		return cliOptions{}, errors.New("unexpected positional arguments")
	}
	if *path == "" {
		return cliOptions{}, errors.New("-config is required")
	}
	return cliOptions{
		path:       *path,
		production: *production,
		sourceRef:  *sourceRef,
	}, nil
}

func loadCatalog(path string) (config.Catalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return config.Catalog{}, fmt.Errorf("open catalog: %w", err)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	closeErr := file.Close()
	if err != nil {
		return config.Catalog{}, errors.New("read catalog: failed")
	}
	if closeErr != nil {
		return config.Catalog{}, errors.New("read catalog: failed to close input")
	}
	if len(data) > maxConfigBytes {
		return config.Catalog{}, fmt.Errorf("read catalog: exceeds %d bytes", maxConfigBytes)
	}
	decoded, err := config.Decode(data)
	if err != nil {
		return config.Catalog{}, fmt.Errorf("invalid catalog: %w", err)
	}
	return decoded, nil
}
