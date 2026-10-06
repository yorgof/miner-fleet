package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type dataRow struct {
	Name  string
	Value string
}
type dataGroup struct {
	Name string
	Rows []dataRow
}

func flatten(prefix string, v any, out *[]dataRow) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			*out = append(*out, dataRow{prefix, "{}"})
		}
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + " · " + k
			}
			flatten(p, x[k], out)
		}
	case []any:
		if len(x) == 0 {
			*out = append(*out, dataRow{prefix, "[]"})
		}
		for i, v := range x {
			flatten(fmt.Sprintf("%s [%d]", prefix, i), v, out)
		}
	case string:
		*out = append(*out, dataRow{prefix, x})
	case nil:
		*out = append(*out, dataRow{prefix, "Unavailable"})
	default:
		b, _ := json.Marshal(v)
		*out = append(*out, dataRow{prefix, string(b)})
	}
}
func grouped(d map[string]any) []dataGroup {
	groups := map[string][]dataRow{}
	for k, v := range d {
		name := "System and device"
		lower := strings.ToLower(k)
		switch {
		case k == "system/statistics":
			name = "Firmware statistics"
		case k == "configuration":
			name = "Configuration"
		case k == "firmware":
			name = "Firmware identity"
		case strings.Contains(lower, "stratum") || strings.Contains(lower, "pool") || strings.Contains(lower, "coinbase") || k == "coin":
			name = "Pools and Bitcoin"
		case strings.Contains(lower, "fan") || strings.Contains(lower, "temp") || strings.HasPrefix(lower, "pid"):
			name = "Cooling"
		case strings.Contains(lower, "hash") || strings.Contains(lower, "share") || strings.Contains(lower, "diff") || strings.Contains(lower, "freq") || strings.Contains(lower, "voltage") || strings.Contains(lower, "power") || k == "summary" || k == "devs" || k == "devdetails" || k == "tunerstatus":
			name = "Mining and performance"
		case strings.Contains(lower, "wifi") || strings.Contains(lower, "ip") || strings.Contains(lower, "ssid") || strings.Contains(lower, "rssi"):
			name = "Network"
		}
		rows := groups[name]
		flatten(k, v, &rows)
		groups[name] = rows
	}
	out := []dataGroup{}
	for name, rows := range groups {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		out = append(out, dataGroup{name, rows})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
