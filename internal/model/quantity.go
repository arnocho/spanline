package model

import (
	"math"
	"strconv"
	"strings"
)

// ParseCPU converts a Kubernetes CPU quantity to millicores. Returns ok=false when unparseable.
func ParseCPU(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	scale := 1000.0
	if strings.HasSuffix(q, "m") {
		q = strings.TrimSuffix(q, "m")
		scale = 1
	}
	v, ok := parseNumber(q)
	if !ok {
		return 0, false
	}
	return wholeUnits(v * scale)
}

// memSuffixes are the Kubernetes quantity suffixes, binary before decimal so that "Mi" is
// tried before "M". Kilo is a lowercase k in Kubernetes; the uppercase K is kept as a
// leniency for hand written fixtures. The m suffix is the milli form the API server uses to
// canonicalise a fractional byte count.
var memSuffixes = []struct {
	suffix string
	mult   float64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
	{"k", 1e3}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18},
	{"m", 1e-3},
}

// ParseMemory converts a Kubernetes memory quantity to bytes. Returns ok=false when unparseable.
func ParseMemory(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	mult := 1.0
	for _, s := range memSuffixes {
		if strings.HasSuffix(q, s.suffix) {
			q = strings.TrimSuffix(q, s.suffix)
			mult = s.mult
			break
		}
	}
	v, ok := parseNumber(q)
	if !ok {
		return 0, false
	}
	return wholeUnits(v * mult)
}

// parseNumber reads the number part of a quantity. strconv alone would also accept "inf",
// "NaN", hexadecimal floats and digit separators, none of which the API server does, and
// int64 of an infinity differs by platform. A negative amount is refused too: the API server
// rejects it, so one can only come from a broken source, and it would shrink a total.
func parseNumber(s string) (float64, bool) {
	if !quantityNumber(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) || v < 0 {
		return 0, false
	}
	return v, true
}

// quantityNumber checks the grammar of a quantity's number as Kubernetes defines it:
// an optional sign, digits with an optional fraction, then an optional decimal exponent.
func quantityNumber(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exp := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			exp++
		}
		if exp == 0 {
			return false
		}
	}
	return i == len(s)
}

// wholeUnits rounds a scaled quantity up to a whole millicore or byte, as Kubernetes does
// when it reads a value, after absorbing the float error a decimal string leaves behind so
// "1.005" cores reads as 1005m and not 1004m. A value beyond int64 is unreadable.
func wholeUnits(x float64) (int64, bool) {
	x = math.Ceil(x - 1e-6)
	if x < 0 {
		x = 0
	}
	if x >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(x), true
}

// memUnits are the units HumanMemory prints, largest first, with the decimals each shows.
var memUnits = []struct {
	suffix string
	size   int64
	prec   int
}{
	{"Ti", 1 << 40, 1}, {"Gi", 1 << 30, 1}, {"Mi", 1 << 20, 0}, {"Ki", 1 << 10, 0},
}

// HumanMemory renders bytes the way an operator expects to read them. A value that would
// round up to a whole of the next unit is printed in that unit, so 1073741823 bytes reads
// as 1.0Gi and never as 1024Mi.
func HumanMemory(b int64) string {
	for i, u := range memUnits {
		if b >= u.size || (i+1 < len(memUnits) && roundsToWhole(b, i+1)) {
			return strconv.FormatFloat(float64(b)/float64(u.size), 'f', u.prec, 64) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// roundsToWhole reports whether b, printed in memUnits[i], would read as 1024 of that unit.
func roundsToWhole(b int64, i int) bool {
	u := memUnits[i]
	if b < u.size {
		return false
	}
	p := math.Pow(10, float64(u.prec))
	return math.Round(float64(b)/float64(u.size)*p)/p >= 1024
}

// HumanCPU renders millicores as cores when the value is a round number of cores.
func HumanCPU(m int64) string {
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatInt(m, 10) + "m"
}
