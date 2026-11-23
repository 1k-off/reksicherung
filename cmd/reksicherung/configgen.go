package main

import (
	"context"
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
	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/resolve"
	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/resticprofile"
)

func runConfiggen(args []string, stdout, stderr io.Writer) error {
	return runConfiggenContext(context.Background(), args, stdout, stderr)
}

func runConfiggenContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("configgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPath := fs.String("in", "", "path to I-CONFIGGEN YAML")
	provider := fs.String("provider", "", "gobackup or resticprofile")
	outPath := fs.String("out", "", "base path; writes <name>.safe.yml and <name>.unsafe.yml")
	bindingsPath := fs.String("bindings", "", "optional path to write bindings JSON (safe placeholders)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inPath) == "" || strings.TrimSpace(*provider) == "" || strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(stderr, "usage: reksicherung configgen -provider gobackup|resticprofile -in <input.yaml> -out <base.yml> [-bindings <bindings.json>]")
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

	var safeYAML []byte
	var bindings []input.Binding
	var sidecars []resticprofile.Sidecar
	switch strings.ToLower(strings.TrimSpace(*provider)) {
	case "gobackup":
		result, err := gobackup.Generate(decoded)
		if err != nil {
			return err
		}
		safeYAML, bindings = result.YAML, result.Bindings
	case "resticprofile":
		result, err := resticprofile.Generate(decoded)
		if err != nil {
			return err
		}
		safeYAML, bindings, sidecars = result.YAML, result.Bindings, result.Sidecars
	default:
		return fmt.Errorf("unsupported provider %q (want gobackup or resticprofile)", *provider)
	}

	bao, err := openBaoProvider(true)
	if err != nil {
		return fmt.Errorf("configgen unsafe render requires OpenBao: %w", err)
	}
	unsafeYAML, err := resolve.UnsafeYAML(ctx, safeYAML, bindings, bao)
	if err != nil {
		return fmt.Errorf("configgen unsafe render: %w", err)
	}

	safePath, unsafePath := configgenOutputPaths(*outPath)
	outDir := filepath.Dir(safePath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(safePath, safeYAML, 0o644); err != nil {
		return fmt.Errorf("write safe config: %w", err)
	}
	if err := os.WriteFile(unsafePath, unsafeYAML, 0o600); err != nil {
		return fmt.Errorf("write unsafe config: %w", err)
	}
	for _, sc := range sidecars {
		if filepath.Base(sc.Name) != sc.Name || strings.Contains(sc.Name, "..") {
			return fmt.Errorf("refusing unsafe sidecar name %q", sc.Name)
		}
		path := filepath.Join(outDir, sc.Name)
		mode := os.FileMode(0o644)
		if strings.HasSuffix(sc.Name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, sc.Content, mode); err != nil {
			return fmt.Errorf("write hook sidecar: %w", err)
		}
		unsafeSidecar, err := resolve.UnsafeText(ctx, sc.Content, bindings, bao)
		if err != nil {
			return fmt.Errorf("configgen unsafe sidecar %s: %w", sc.Name, err)
		}
		unsafeSidecarPath := filepath.Join(outDir, configgenUnsafeSidecarName(sc.Name))
		if err := os.WriteFile(unsafeSidecarPath, unsafeSidecar, 0o600); err != nil {
			return fmt.Errorf("write unsafe hook sidecar: %w", err)
		}
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
	fmt.Fprintf(stdout, "wrote safe %s and unsafe %s (%d bindings", safePath, unsafePath, len(bindings))
	if len(sidecars) > 0 {
		fmt.Fprintf(stdout, ", %d hook scripts", len(sidecars))
	}
	fmt.Fprintln(stdout, ")")
	return nil
}

func configgenOutputPaths(out string) (safePath, unsafePath string) {
	ext := filepath.Ext(out)
	stem := strings.TrimSuffix(out, ext)
	if ext == "" {
		ext = ".yml"
	}
	return stem + ".safe" + ext, stem + ".unsafe" + ext
}

func configgenUnsafeSidecarName(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == "" {
		return stem + ".unsafe"
	}
	return stem + ".unsafe" + ext
}
