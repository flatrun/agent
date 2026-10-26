package models

import "testing"

func TestParseBodySize(t *testing.T) {
	cases := map[string]int64{"0": 0, "1024": 1024, "512k": 512 << 10, "64m": 64 << 20, "64M": 64 << 20, "10g": 10 << 30}
	for in, want := range cases {
		got, err := ParseBodySize(in)
		if err != nil || got != want {
			t.Errorf("ParseBodySize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "64mb", "1.5m", "-1", "11g", "m", "64 m", "99999999999999999999"} {
		if _, err := ParseBodySize(bad); err == nil {
			t.Errorf("ParseBodySize(%q) accepted an invalid size", bad)
		}
	}
}
