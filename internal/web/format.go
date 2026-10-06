package web

import "fmt"

func fmtHashrate(ghs float64) string {
	if ghs <= 0 {
		return "-"
	}
	scale, unit := hashrateUnit(ghs)
	if unit == "TH/s" {
		return fmt.Sprintf("%.2f %s", ghs/scale, unit)
	}
	return fmt.Sprintf("%.1f %s", ghs/scale, unit)
}

func hashrateUnit(ghs float64) (float64, string) {
	switch {
	case ghs >= 1000:
		return 1000, "TH/s"
	case ghs >= 1:
		return 1, "GH/s"
	case ghs >= 1e-3:
		return 1e-3, "MH/s"
	case ghs >= 1e-6:
		return 1e-6, "kH/s"
	default:
		return 1e-9, "H/s"
	}
}

func fmtTemp(c float64) string {
	if c == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f°C", c)
}

func fmtPower(w float64) string {
	if w <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f W", w)
}

func fmtFan(percent float64, rpm int) string {
	switch {
	case percent > 0 && rpm > 0:
		return fmt.Sprintf("%.0f%% (%d rpm)", percent, rpm)
	case percent > 0:
		return fmt.Sprintf("%.0f%%", percent)
	case rpm > 0:
		return fmt.Sprintf("%d rpm", rpm)
	default:
		return "-"
	}
}

// fmtDiff matches the K/M/G/T-suffixed style AxeOS's own web UI and the
// Public Pool dashboard already use for difficulty numbers, confirmed
// against real device values on 2026-08-24 (e.g. 4534224060 -> "4.53 G").
func fmtDiff(v float64) string {
	if v <= 0 {
		return "-"
	}
	switch {
	case v >= 1e12:
		return fmt.Sprintf("%.2f T", v/1e12)
	case v >= 1e9:
		return fmt.Sprintf("%.2f G", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2f M", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.2f K", v/1e3)
	case v < 1:
		return fmt.Sprintf("%.6g", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func fmtDuration(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	d := seconds
	days := d / 86400
	d %= 86400
	hours := d / 3600
	d %= 3600
	minutes := d / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
