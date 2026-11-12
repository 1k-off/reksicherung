package main

import (
	"strings"
	"testing"
)

func TestLoadConfigRequiresOpenBaoOnly(t *testing.T) {
	valid := map[string]string{
		openBaoAddr:  "https://bao.example.invalid",
		openBaoToken: "private-token",
	}
	if _, err := loadConfig(func(name string) string { return valid[name] }); err != nil {
		t.Fatalf("loadConfig(valid) error = %v", err)
	}

	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing token", change: func(values map[string]string) { delete(values, openBaoToken) }},
		{name: "remote plaintext OpenBao", change: func(values map[string]string) { values[openBaoAddr] = "http://bao.example.invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{openBaoAddr: valid[openBaoAddr], openBaoToken: valid[openBaoToken]}
			test.change(values)
			_, err := loadConfig(func(name string) string { return values[name] })
			if err == nil {
				t.Fatal("loadConfig() succeeded with invalid configuration")
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatalf("loadConfig() leaked secret material: %v", err)
			}
		})
	}
}

func TestLoadConfigAllowsLoopbackOpenBao(t *testing.T) {
	values := map[string]string{
		openBaoAddr:  "http://127.0.0.1:8200",
		openBaoToken: "private-token",
	}
	if _, err := loadConfig(func(name string) string { return values[name] }); err != nil {
		t.Fatalf("loadConfig(loopback OpenBao) error = %v", err)
	}
}
