package cli

import "testing"

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
