package lvm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner records arguments for each Run call and returns canned
// stdout/err pairs from a per-binary script.
type fakeRunner struct {
	calls []call
	// outputs maps the first arg (binary name) to a list of canned
	// responses, returned in order. Useful when the same binary is
	// invoked multiple times in a test.
	outputs map[string][]canned
}

type call struct {
	name string
	args []string
}

type canned struct {
	stdout string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name: name, args: append([]string{}, args...)})
	if list, ok := f.outputs[name]; ok && len(list) > 0 {
		c := list[0]
		f.outputs[name] = list[1:]
		return []byte(c.stdout), c.err
	}
	return nil, nil
}

func TestCreateLV(t *testing.T) {
	r := &fakeRunner{}
	m := New(r)
	if err := m.CreateLV(context.Background(), "vg-sltv", "data1", 10*1024*1024*1024); err != nil {
		t.Fatalf("CreateLV: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	c := r.calls[0]
	if c.name != "lvcreate" {
		t.Errorf("binary = %s", c.name)
	}
	wantArgs := []string{"-L", "10737418240B", "-n", "data1", "-y", "vg-sltv"}
	if strings.Join(c.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
}

func TestCreateLVValidation(t *testing.T) {
	m := New(&fakeRunner{})
	if err := m.CreateLV(context.Background(), "", "x", 1); err == nil {
		t.Errorf("expected error for empty vg")
	}
	if err := m.CreateLV(context.Background(), "v", "", 1); err == nil {
		t.Errorf("expected error for empty name")
	}
	if err := m.CreateLV(context.Background(), "v", "x", 0); err == nil {
		t.Errorf("expected error for zero size")
	}
}

func TestExtendLVPercent(t *testing.T) {
	r := &fakeRunner{}
	m := New(r)
	if err := m.ExtendLVPercent(context.Background(), "vg", "data1", 10); err != nil {
		t.Fatalf("ExtendLVPercent: %v", err)
	}
	c := r.calls[0]
	if c.name != "lvextend" {
		t.Errorf("binary = %s", c.name)
	}
	wantArgs := []string{"-l", "+10%LV", "vg/data1"}
	if strings.Join(c.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
}

func TestRemoveLV(t *testing.T) {
	r := &fakeRunner{}
	m := New(r)
	if err := m.RemoveLV(context.Background(), "vg", "data1"); err != nil {
		t.Fatalf("RemoveLV: %v", err)
	}
	c := r.calls[0]
	if c.name != "lvremove" || c.args[len(c.args)-1] != "vg/data1" {
		t.Errorf("RemoveLV call = %+v", c)
	}
}

func TestListVGs(t *testing.T) {
	out := `{"report":[{"vg":[{"vg_name":"vg-sltv","vg_size":"107374182400","vg_free":"96636764160"}]}]}`
	r := &fakeRunner{outputs: map[string][]canned{"vgs": {{stdout: out}}}}
	m := New(r)
	vgs, err := m.ListVGs(context.Background())
	if err != nil {
		t.Fatalf("ListVGs: %v", err)
	}
	if len(vgs) != 1 || vgs[0].Name != "vg-sltv" || vgs[0].SizeBytes != 107374182400 || vgs[0].FreeBytes != 96636764160 {
		t.Errorf("vgs = %+v", vgs)
	}
}

func TestListLVs(t *testing.T) {
	out := `{"report":[{"lv":[{"lv_name":"data1","vg_name":"vg-sltv","lv_size":"10737418240","lv_path":"/dev/vg-sltv/data1"}]}]}`
	r := &fakeRunner{outputs: map[string][]canned{"lvs": {{stdout: out}}}}
	m := New(r)
	lvs, err := m.ListLVs(context.Background(), "vg-sltv")
	if err != nil {
		t.Fatalf("ListLVs: %v", err)
	}
	if len(lvs) != 1 || lvs[0].Name != "data1" || lvs[0].Path != "/dev/vg-sltv/data1" {
		t.Errorf("lvs = %+v", lvs)
	}
}

func TestRunnerErrorWrapped(t *testing.T) {
	r := &fakeRunner{outputs: map[string][]canned{"lvcreate": {{stdout: "boom", err: errors.New("exit 5")}}}}
	m := New(r)
	err := m.CreateLV(context.Background(), "vg", "x", 1024)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDevicePath(t *testing.T) {
	if got := DevicePath("vg-sltv", "x"); got != "/dev/vg-sltv/x" {
		t.Errorf("DevicePath = %s", got)
	}
}
