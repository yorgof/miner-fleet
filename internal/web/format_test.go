package web

import "testing"

func TestHashrateUnits(t *testing.T) {
	for _, tc := range []struct {
		ghs  float64
		want string
	}{
		{0, "-"}, {10e-9, "10.0 H/s"}, {27e-6, "27.0 kH/s"},
		{0.0010618, "1.1 MH/s"}, {682.9, "682.9 GH/s"}, {4870, "4.87 TH/s"},
	} {
		if got := fmtHashrate(tc.ghs); got != tc.want {
			t.Errorf("%g GH/s: got %q want %q", tc.ghs, got, tc.want)
		}
	}
}
