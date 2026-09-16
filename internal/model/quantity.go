package model

import (
	"strconv"
	"strings"
)

// ParseCPU converts a Kubernetes CPU quantity to millicores. Returns ok=false when unparseable.
func ParseCPU(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if strings.HasSuffix(q, "m") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(q, "m"), 64)
		if err != nil {
			return 0, false
		}
		return int64(v), true
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0, false
	}
	return int64(v * 1000), true
}

var memSuffixes = []struct {
	suffix string
	mult   int64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
	{"K", 1000}, {"M", 1000 * 1000}, {"G", 1000 * 1000 * 1000}, {"T", 1000 * 1000 * 1000 * 1000},
}

// ParseMemory converts a Kubernetes memory quantity to bytes. Returns ok=false when unparseable.
func ParseMemory(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	for _, s := range memSuffixes {
		if strings.HasSuffix(q, s.suffix) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(q, s.suffix), 64)
			if err != nil {
				return 0, false
			}
			return int64(v * float64(s.mult)), true
		}
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0, false
	}
	return int64(v), true
}

// HumanMemory renders bytes the way an operator expects to read them.
func HumanMemory(b int64) string {
	switch {
	case b >= 1<<30:
		return strconv.FormatFloat(float64(b)/float64(1<<30), 'f', 1, 64) + "Gi"
	case b >= 1<<20:
		return strconv.FormatFloat(float64(b)/float64(1<<20), 'f', 0, 64) + "Mi"
	case b >= 1<<10:
		return strconv.FormatFloat(float64(b)/float64(1<<10), 'f', 0, 64) + "Ki"
	default:
		return strconv.FormatInt(b, 10)
	}
}

// HumanCPU renders millicores as cores when the value is a round number of cores.
func HumanCPU(m int64) string {
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatInt(m, 10) + "m"
}
