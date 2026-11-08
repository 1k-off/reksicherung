package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLICleanupAndReconcileRequireFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "cleanup missing flags", args: []string{"cleanup"}, want: "usage: reksicherung cleanup"},
		{name: "reconcile missing registry", args: []string{"reconcile"}, want: "usage: reksicherung reconcile"},
		{name: "reconcile invalid interval via once missing registry", args: []string{"reconcile", "-once"}, want: "usage: reksicherung reconcile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%v) error = %v, want substring %q", test.args, err, test.want)
			}
		})
	}
}

func TestCLIHelpAdvertisesDatabaseCleanupNotContainers(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}
	text := strings.ToLower(stdout.String())
	for _, required := range []string{"cleanup", "reconcile", "hold_for_review"} {
		if !strings.Contains(text, required) {
			t.Errorf("help missing %q: %s", required, stdout.String())
		}
	}
	for _, forbidden := range []string{"podman", "docker compose", "remove container"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("help advertises container management %q: %s", forbidden, stdout.String())
		}
	}
}
