package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelp(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"sctl", "create-disk", "attach-disk", "status"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestVersionCmd(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute version: %v", err)
	}
	if buf.Len() == 0 {
		t.Errorf("version produced no output")
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0K"},
		{1024 * 1024, "1.0M"},
		{1024 * 1024 * 1024, "1.0G"},
	}
	for _, tc := range cases {
		got := humanSize(tc.in)
		if got != tc.want {
			t.Errorf("humanSize(%d) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
