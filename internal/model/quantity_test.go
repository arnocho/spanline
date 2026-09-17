package model

import "testing"

func TestParseCPU(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"100m", 100, true},
		{"0.5", 500, true},
		{"1", 1000, true},
		{"2", 2000, true},
		{"1500m", 1500, true},
		{"2.5", 2500, true},
		{"1e3", 1000000, true},
		{"1E3", 1000000, true},
		{"1e3m", 1000, true},
		{".5", 500, true},
		{" 250m ", 250, true},
		{"250m\n", 250, true},
		{"0", 0, true},
		{"", 0, false},
		{"   ", 0, false},
		{"m", 0, false},
		{"abc", 0, false},
		{"1 core", 0, false},
		{"-1", 0, false},
		{"-100m", 0, false},
		{"inf", 0, false},
		{"Inf", 0, false},
		{"NaN", 0, false},
		{"1_000m", 0, false},
		{"0x1p-2", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseCPU(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseCPU(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseMemory(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1Ki", 1 << 10, true},
		{"1Mi", 1 << 20, true},
		{"1Gi", 1 << 30, true},
		{"1Ti", 1 << 40, true},
		{"1Pi", 1 << 50, true},
		{"1Ei", 1 << 60, true},
		{"1k", 1000, true},
		{"512k", 512000, true},
		{"1K", 1000, true},
		{"1M", 1000 * 1000, true},
		{"1G", 1000 * 1000 * 1000, true},
		{"1T", 1000 * 1000 * 1000 * 1000, true},
		{"1P", 1000 * 1000 * 1000 * 1000 * 1000, true},
		{"1E", 1000 * 1000 * 1000 * 1000 * 1000 * 1000, true},
		{"1e3", 1000, true},
		{"1e9", 1000000000, true},
		{"2.5Gi", 2684354560, true},
		{"0.5Gi", 536870912, true},
		{"1.5M", 1500000, true},
		{"128974848", 128974848, true},
		{"12Gi", 12 << 30, true},
		{" 1Gi\n", 1 << 30, true},
		{"0", 0, true},
		{"", 0, false},
		{"  ", 0, false},
		{"Gi", 0, false},
		{"1 Gi", 0, false},
		{"1.5.2Gi", 0, false},
		{"-1Gi", 0, false},
		{"-1", 0, false},
		{"inf", 0, false},
		{"NaN", 0, false},
		{"1_024Mi", 0, false},
		{"0x10", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseMemory(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestParseMemoryDistinguishesDecimalFromBinary pins the one confusion an operator would not
// forgive: 1M is a million bytes, 1Mi is 1048576, and the two must never be read as the same.
func TestParseMemoryDistinguishesDecimalFromBinary(t *testing.T) {
	m, _ := ParseMemory("1M")
	mi, _ := ParseMemory("1Mi")
	g, _ := ParseMemory("1G")
	gi, _ := ParseMemory("1Gi")
	if m == mi || g == gi {
		t.Fatalf("decimal and binary suffixes read the same: 1M=%d 1Mi=%d 1G=%d 1Gi=%d", m, mi, g, gi)
	}
	if m != 1000000 || mi != 1048576 {
		t.Errorf("1M=%d, 1Mi=%d; want 1000000 and 1048576", m, mi)
	}
}

func TestHumanMemory(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{512, "512"},
		{1023, "1023"},
		{1024, "1Ki"},
		{1536, "2Ki"},
		{1 << 20, "1Mi"},
		{500 << 20, "500Mi"},
		{1 << 30, "1.0Gi"},
		{12 << 30, "12.0Gi"},
		{1610612736, "1.5Gi"},
		{1 << 40, "1.0Ti"},
		// Values that round up to a whole unit must move to that unit, never print 1024 of the smaller one.
		{1048064, "1Mi"},
		{1048575, "1Mi"},
		{1073217536, "1.0Gi"},
		{1073741823, "1.0Gi"},
		{(1 << 40) - 1, "1.0Ti"},
	}
	for _, c := range cases {
		if got := HumanMemory(c.in); got != c.want {
			t.Errorf("HumanMemory(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanCPU(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{500, "500m"},
		{1000, "1"},
		{1500, "1500m"},
		{2000, "2"},
		{3860, "3860m"},
	}
	for _, c := range cases {
		if got := HumanCPU(c.in); got != c.want {
			t.Errorf("HumanCPU(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
