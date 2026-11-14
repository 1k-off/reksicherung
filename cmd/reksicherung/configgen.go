package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/gobackup"
	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/resticprofile"
)

func runConfiggen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("configgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPath := fs.String("in", "", "path to I-CONFIGGEN YAML")
	provider := fs.String("provider", "", "gobackup or resticprofile")
	outPath := fs.String("out", "", "path to write generated provider YAML")
	bindingsPath := fs.String("bindings", "", "optional path to write bindings JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inPath) == "" || strings.TrimSpace(*provider) == "" || strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(stderr, "usage: reksicherung configgen -provider gobackup|resticprofile -in <input.yaml> -out <output.yml> [-bindings <bindings.json>]")
		return errors.New("configgen requires -provider, -in, and -out")
	}

	file, err := os.Open(*inPath)
	if err != nil {
		return fmt.Errorf("open configgen input: %w", err)
	}
	defer file.Close()

	decoded, err := input.Decode(file)
	if err != nil {
		return err
	}
	if err := decoded.Validate(); err != nil {
		return err
	}

	var yamlBytes []byte
	var bindings []input.Binding
	switch strings.ToLower(strings.TrimSpace(*provider)) {
	case "gobackup":
		result, err := gobackup.Generate(decoded)
		if err != nil {
			return err
		}
		yamlBytes, bindings = result.YAML, result.Bindings
	case "resticprofile":
		result, err := resticprofile.Generate(decoded)
		if err != nil {
			return err
		}
		yamlBytes, bindings = result.YAML, result.Bindings
	default:
		return fmt.Errorf("unsupported provider %q (want gobackup or resticprofile)", *provider)
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(*outPath, yamlBytes, 0o644); err != nil {
		return fmt.Errorf("write generated config: %w", err)
	}
	if strings.TrimSpace(*bindingsPath) != "" {
		if err := os.MkdirAll(filepath.Dir(*bindingsPath), 0o755); err != nil {
			return fmt.Errorf("create bindings directory: %w", err)
		}
		encoded, err := json.MarshalIndent(bindings, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal bindings: %w", err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(*bindingsPath, encoded, 0o644); err != nil {
			return fmt.Errorf("write bindings: %w", err)
		}
	}
	fmt.Fprintf(stdout, "wrote %s (%d bindings)\n", *outPath, len(bindings))
	return nil
}
