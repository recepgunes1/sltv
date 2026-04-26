// Package lvm wraps the lvm2 user-space tools (lvcreate, lvextend,
// lvremove, lvs, vgs) behind a small interface so the rest of sltvd
// does not need to shell out directly. It also exposes a Runner
// indirection so unit tests can substitute a fake command runner.
//
// The wrapper deliberately speaks JSON wherever lvm2 supports it
// (`--reportformat json` is available in modern lvm2 releases) which
// makes parsing robust and avoids brittle text scraping.
package lvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner is the minimal command-execution surface the package needs.
// It returns the combined stdout/stderr output and an error suitable
// for wrapping, allowing fakes to be defined in tests without invoking
// real binaries.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner uses os/exec to actually run a binary.
type ExecRunner struct{}

// Run executes the command and returns combined output. A non-zero
// exit code is wrapped together with the captured output so callers
// can include lvm's own error messages in user-facing errors.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Manager talks to lvm2 via Runner.
type Manager struct {
	r Runner
}

// New constructs a Manager. Pass ExecRunner{} for production use.
func New(r Runner) *Manager {
	if r == nil {
		r = ExecRunner{}
	}
	return &Manager{r: r}
}

// VG describes a volume group as returned by `vgs --reportformat json`.
type VG struct {
	Name      string
	SizeBytes uint64
	FreeBytes uint64
}

// LV describes a logical volume as returned by `lvs --reportformat json`.
type LV struct {
	Name      string
	VG        string
	SizeBytes uint64
	Path      string
}

// ListVGs returns the volume groups visible on this host.
func (m *Manager) ListVGs(ctx context.Context) ([]VG, error) {
	out, err := m.r.Run(ctx, "vgs", "--reportformat", "json", "--units", "b", "--nosuffix",
		"-o", "vg_name,vg_size,vg_free")
	if err != nil {
		return nil, err
	}
	return parseVGsJSON(out)
}

// ListLVs returns logical volumes, optionally filtered by VG (vg=="" => all).
func (m *Manager) ListLVs(ctx context.Context, vg string) ([]LV, error) {
	args := []string{"--reportformat", "json", "--units", "b", "--nosuffix",
		"-o", "lv_name,vg_name,lv_size,lv_path"}
	if vg != "" {
		args = append(args, vg)
	}
	out, err := m.r.Run(ctx, "lvs", args...)
	if err != nil {
		return nil, err
	}
	return parseLVsJSON(out)
}

// CreateLV provisions a new logical volume of the given byte size.
// Equivalent to `lvcreate -L <size>B -n <name> <vg>`. Use 0 for size
// to defer to lvcreate (rare).
func (m *Manager) CreateLV(ctx context.Context, vg, name string, sizeBytes uint64) error {
	if vg == "" || name == "" {
		return fmt.Errorf("lvm.CreateLV: vg and name are required")
	}
	if sizeBytes == 0 {
		return fmt.Errorf("lvm.CreateLV: size must be > 0")
	}
	_, err := m.r.Run(ctx, "lvcreate",
		"-L", fmt.Sprintf("%dB", sizeBytes),
		"-n", name,
		"-y",
		vg,
	)
	return err
}

// RemoveLV deletes a logical volume. Equivalent to `lvremove -f <vg>/<name>`.
func (m *Manager) RemoveLV(ctx context.Context, vg, name string) error {
	_, err := m.r.Run(ctx, "lvremove", "-f", fmt.Sprintf("%s/%s", vg, name))
	return err
}

// ExtendLVAbsolute grows or sets the LV to the given absolute byte
// size. Equivalent to `lvextend -L <size>B <vg>/<name>`.
func (m *Manager) ExtendLVAbsolute(ctx context.Context, vg, name string, sizeBytes uint64) error {
	_, err := m.r.Run(ctx, "lvextend",
		"-L", fmt.Sprintf("%dB", sizeBytes),
		fmt.Sprintf("%s/%s", vg, name),
	)
	return err
}

// ExtendLVDelta grows the LV by deltaBytes. Equivalent to `lvextend
// -L +<delta>B <vg>/<name>`.
func (m *Manager) ExtendLVDelta(ctx context.Context, vg, name string, deltaBytes uint64) error {
	_, err := m.r.Run(ctx, "lvextend",
		"-L", fmt.Sprintf("+%dB", deltaBytes),
		fmt.Sprintf("%s/%s", vg, name),
	)
	return err
}

// ExtendLVPercent grows the LV by the given percent of its current
// size. Equivalent to `lvextend -l +<pct>%LV <vg>/<name>`.
func (m *Manager) ExtendLVPercent(ctx context.Context, vg, name string, percent int) error {
	if percent <= 0 {
		return fmt.Errorf("lvm.ExtendLVPercent: percent must be > 0")
	}
	_, err := m.r.Run(ctx, "lvextend",
		"-l", fmt.Sprintf("+%d%%LV", percent),
		fmt.Sprintf("%s/%s", vg, name),
	)
	return err
}

// DevicePath returns the canonical device path of the LV.
// It does not perform any I/O; it just builds the conventional path.
func DevicePath(vg, name string) string {
	return fmt.Sprintf("/dev/%s/%s", vg, name)
}

// --- JSON parsing helpers --------------------------------------------------

// lvm2 emits reports of the form:
//
//	{
//	  "report": [
//	    {
//	      "vg": [{"vg_name":"vg-sltv","vg_size":"...","vg_free":"..."}]
//	    }
//	  ]
//	}
//
// Sizes are byte counts (we pass --units b --nosuffix) but lvm2 still
// returns them as JSON strings, which is why we decode into json.Number
// and parse explicitly.
type lvmReport struct {
	Report []map[string][]map[string]json.Number `json:"report"`
}

func decodeReport(out []byte) (lvmReport, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	var r lvmReport
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("parse lvm json: %w; raw=%s", err, strings.TrimSpace(string(out)))
	}
	return r, nil
}

// decodeStringReport is a variant for reports whose fields are strings
// (lv_path, vg_name, lv_name) rather than numbers; we use a generic
// map[string]any because lvm mixes both shapes.
type lvmReportAny struct {
	Report []map[string][]map[string]any `json:"report"`
}

func decodeReportAny(out []byte) (lvmReportAny, error) {
	var r lvmReportAny
	if err := json.Unmarshal(out, &r); err != nil {
		return r, fmt.Errorf("parse lvm json: %w; raw=%s", err, strings.TrimSpace(string(out)))
	}
	return r, nil
}

func parseVGsJSON(out []byte) ([]VG, error) {
	r, err := decodeReportAny(out)
	if err != nil {
		return nil, err
	}
	var vgs []VG
	for _, sec := range r.Report {
		rows := sec["vg"]
		for _, row := range rows {
			name, _ := row["vg_name"].(string)
			size, err := parseUintField(row["vg_size"])
			if err != nil {
				return nil, fmt.Errorf("vg_size: %w", err)
			}
			free, err := parseUintField(row["vg_free"])
			if err != nil {
				return nil, fmt.Errorf("vg_free: %w", err)
			}
			vgs = append(vgs, VG{Name: name, SizeBytes: size, FreeBytes: free})
		}
	}
	return vgs, nil
}

func parseLVsJSON(out []byte) ([]LV, error) {
	r, err := decodeReportAny(out)
	if err != nil {
		return nil, err
	}
	var lvs []LV
	for _, sec := range r.Report {
		rows := sec["lv"]
		for _, row := range rows {
			name, _ := row["lv_name"].(string)
			vg, _ := row["vg_name"].(string)
			path, _ := row["lv_path"].(string)
			size, err := parseUintField(row["lv_size"])
			if err != nil {
				return nil, fmt.Errorf("lv_size: %w", err)
			}
			lvs = append(lvs, LV{Name: name, VG: vg, SizeBytes: size, Path: path})
		}
	}
	return lvs, nil
}

func parseUintField(v any) (uint64, error) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, err
		}
		return n, nil
	case json.Number:
		return strconv.ParseUint(t.String(), 10, 64)
	case float64:
		return uint64(t), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
