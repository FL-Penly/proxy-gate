package main

import "testing"

func TestParseConfigPath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "equals", args: []string{"--config=/tmp/proxy.toml"}, want: "/tmp/proxy.toml"},
		{name: "separate", args: []string{"--config", "/tmp/proxy.toml"}, want: "/tmp/proxy.toml"},
		{name: "default", args: nil, want: "./config.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseConfigPath(tt.args); got != tt.want {
				t.Fatalf("parseConfigPath(%v)=%q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
