package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelp(t *testing.T) {
	cmd := New().RootCmd()
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
	cmd := New().RootCmd()
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
