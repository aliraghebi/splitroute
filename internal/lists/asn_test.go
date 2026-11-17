package lists

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// testdata/asn.txt: lines from a real routing-rules asn.txt.gz (AS13335 shortened).
func TestParseASN(t *testing.T) {
	raw, err := os.ReadFile("testdata/asn.txt")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := ParseASN(gz(t, raw), map[uint32]bool{13335: true, 58224: true, 3: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m[2]; ok {
		t.Error("parsed an AS that wasn't asked for")
	}
	if _, ok := m[3]; ok {
		t.Error("AS3 isn't in the file")
	}
	cf := m[13335]
	if len(cf.V4) != 11 || cf.V4[1] != netip.MustParsePrefix("1.1.1.0/24") || len(cf.V6) == 0 {
		t.Errorf("AS13335 %+v", cf)
	}
	if tci := m[58224]; len(tci.V4) != 8 || tci.V4[0] != netip.MustParsePrefix("2.176.0.0/15") {
		t.Errorf("AS58224 %+v", tci)
	}
}

func TestParseASNRefuses(t *testing.T) {
	want := map[uint32]bool{64496: true}
	for name, in := range map[string]string{
		"too broad":  "AS64496 4.0.0.0/7\n",
		"bad prefix": "AS64496 4.0.0.0/33\n",
		"bad AS":     "64496 4.0.0.0/24\n",
	} {
		if _, _, err := ParseASN(gz(t, []byte(in)), want); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, _, err := ParseASN([]byte("AS1 1.0.0.0/24\n"), want); err == nil {
		t.Error("not gzip: want error")
	}
	// A too-broad prefix of an AS nobody asked for isn't parsed (and can't hurt).
	if _, _, err := ParseASN(gz(t, []byte("AS64497 4.0.0.0/7\nAS64496 198.51.100.0/24\n")), want); err != nil {
		t.Errorf("unrelated line: %v", err)
	}
	// Reserved ranges are skipped, not fatal.
	m, st, err := ParseASN(gz(t, []byte("AS64496 10.1.0.0/16 198.51.100.0/24\n")), want)
	if err != nil || st.Reserved != 1 || len(m[64496].V4) != 1 {
		t.Errorf("reserved: %+v %+v %v", m, st, err)
	}
	// Gzip bomb: more than the cap unpacked.
	big := gz(t, append([]byte("AS64496 198.51.100.0/24\n"), bytes.Repeat([]byte("# padding line\n"), maxASNText/15+1)...))
	if _, _, err := ParseASN(big, want); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("bomb: %v", err)
	}
}
