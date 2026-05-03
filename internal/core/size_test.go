package core

import "testing"

func TestBytesString(t *testing.T) {
	cases := []struct {
		in   Bytes
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{Bytes(2.5 * 1024 * 1024 * 1024), "2.5 GB"},
		{Bytes(3 * 1024 * 1024 * 1024 * 1024), "3.0 TB"},
	}
	for _, c := range cases {
		got := c.in.String()
		if got != c.want {
			t.Errorf("Bytes(%d).String() = %q, want %q", int64(c.in), got, c.want)
		}
	}
}

func TestBytesParse(t *testing.T) {
	cases := []struct {
		in   string
		want Bytes
	}{
		{"100", 100},
		{"1KB", 1024},
		{"1.5MB", 1024*1024 + 1024*512},
		{"2GB", 2 * 1024 * 1024 * 1024},
	}
	for _, c := range cases {
		got, err := ParseBytes(c.in)
		if err != nil {
			t.Errorf("ParseBytes(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseBytes(%q) = %d, want %d", c.in, int64(got), int64(c.want))
		}
	}
}
