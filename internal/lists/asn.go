package lists

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"

	"github.com/aliraghebi/splitroute/internal/rules"
)

// ASNFile is the routing-rules release file with every AS number and the
// prefixes it originates: "AS13335 1.0.0.0/24 1.1.1.0/24 ..." per line, gzipped.
const ASNFile = "asn.txt.gz"

// maxASNText caps the gunzipped file (about 12 MB today).
const maxASNText = 128 << 20

// FetchASN downloads asn.txt.gz and checks it against the release's
// SHA256SUMS, falling back to the cached copy like Fetch.
func FetchASN(base, cacheDir string, save bool) ([]byte, []string, error) {
	var warn []string
	var sums map[string]string
	if b, err := httpGet(base + "/SHA256SUMS"); err != nil {
		warn = append(warn, fmt.Sprintf("download SHA256SUMS: %v", err))
	} else {
		sums = parseSums(b)
	}
	b, w, err := fetchFile(base, cacheDir, ASNFile, sums, save)
	return b, append(warn, w...), err
}

// ParseASN returns the entries of the wanted AS numbers. Like a release list,
// a prefix broader than /8 (IPv4) or /16 (IPv6) refuses the whole file, and
// reserved ranges are skipped and counted. Lines of other ASes are not parsed.
func ParseASN(gz []byte, want map[uint32]bool) (map[uint32]rules.Entries, Stats, error) {
	var st Stats
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, st, fmt.Errorf("%s: %w", ASNFile, err)
	}
	r := &capReader{r: zr, left: maxASNText}
	out := map[uint32]rules.Entries{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20) // the biggest AS line is ~300 KB
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		head, rest, _ := strings.Cut(line, " ")
		num, ok := strings.CutPrefix(head, "AS")
		as, err := strconv.ParseUint(num, 10, 32)
		if !ok || err != nil {
			return nil, st, fmt.Errorf("%s: bad line starting %q", ASNFile, head)
		}
		if !want[uint32(as)] {
			continue
		}
		es := out[uint32(as)]
		for _, f := range strings.Fields(rest) {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, st, fmt.Errorf("%s: AS%d: %w", ASNFile, as, err)
			}
			if err := addPrefix(&es, &st, p); err != nil {
				return nil, st, fmt.Errorf("%s: AS%d: %w", ASNFile, as, err)
			}
		}
		out[uint32(as)] = es
	}
	if err := sc.Err(); err != nil {
		return nil, st, fmt.Errorf("%s: %w", ASNFile, err)
	}
	if r.over {
		return nil, st, fmt.Errorf("%s: larger than %d MB unpacked", ASNFile, maxASNText>>20)
	}
	return out, st, nil
}

// capReader stops a gzip bomb: it reads at most left bytes, then reports
// EOF and sets over if there was more.
type capReader struct {
	r    io.Reader
	left int64
	over bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			c.over = true
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
