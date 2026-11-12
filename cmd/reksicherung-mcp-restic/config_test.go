package main

import (
	"strings"
	"testing"
)

func TestLoadConfigRequiresOpenBaoOnly(t *testing.T) {
	valid := map[string]string{
		openBaoAddrEnv:  "https://bao.example.invalid",
		openBaoTokenEnv: "private-token",
	}
	if _, err := loadConfig(func(name string) string { return valid[name] }); err != nil {
		t.Fatalf("loadConfig(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing token", change: func(values map[string]string) { delete(values, openBaoTokenEnv) }},
		{name: "remote plaintext", change: func(values map[string]string) { values[openBaoAddrEnv] = "http://bao.example.invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{openBaoAddrEnv: valid[openBaoAddrEnv], openBaoTokenEnv: valid[openBaoTokenEnv]}
			test.change(values)
			_, err := loadConfig(func(name string) string { return values[name] })
			if err == nil {
				t.Fatal("loadConfig() succeeded")
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatalf("leaked token: %v", err)
			}
		})
	}
}
