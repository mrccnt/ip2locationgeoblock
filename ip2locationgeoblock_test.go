// SPDX-License-Identifier: Apache-2.0

package ip2locationgeoblock

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	okCode     = http.StatusOK
	deniedCode = http.StatusForbidden
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type row struct{ from, to, cc string }

// num converts an IP address to the decimal 128-bit string used in the
// IP2Location CSV files. IPv4 addresses become IPv4-mapped IPv6 numbers
// (::ffff:a.b.c.d), exactly like in IP2LOCATION-LITE-DB1.IPV6.CSV.
func num(t *testing.T, ip string) string {
	t.Helper()
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("invalid test IP %q", ip)
	}
	return new(big.Int).SetBytes(parsed.To16()).String()
}

// writeCSV writes the rows in IP2Location CSV format (no header) and returns the path.
func writeCSV(t *testing.T, dir, name string, rows []row) string {
	t.Helper()
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "\"%s\",\"%s\",\"%s\",\"Test\"\n", num(t, r.from), num(t, r.to), r.cc)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func standardRows() []row {
	return []row{
		{"1.0.0.0", "1.0.0.255", "AU"},
		{"5.0.0.0", "5.255.255.255", "RU"},
		{"8.8.8.0", "8.8.8.255", "US"},
		{"80.0.0.0", "80.255.255.255", "DE"},
		{"90.0.0.0", "90.0.0.255", "-"},
		{"2001:db8::", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "FR"},
		{"2400::", "2400:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "CN"},
	}
}

func testDBPath(t *testing.T) string {
	t.Helper()
	return writeCSV(t, t.TempDir(), "db.csv", standardRows())
}

func baseConfig(path string) *Config {
	cfg := CreateConfig()
	cfg.DatabaseFilePath = path
	cfg.Countries = "RU"
	return cfg
}

type result struct {
	code       int
	nextCalled bool
	body       string
}

func serve(t *testing.T, cfg *Config, remote string, headers map[string]string) result {
	t.Helper()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return result{code: rec.Code, nextCalled: called, body: rec.Body.String()}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func TestCreateConfigDefaults(t *testing.T) {
	cfg := CreateConfig()
	if cfg.Mode != "block" || cfg.AllowPrivate != "true" || cfg.BlockUnknown != "false" || cfg.StatusCode != "403" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" a, b ,,c ")
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("got %v", got)
	}
	if got := splitList(""); len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

func TestIsTrue(t *testing.T) {
	for _, s := range []string{"true", "TRUE", " true ", "1", "t"} {
		if !isTrue(s) {
			t.Errorf("isTrue(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"false", "", "no", "x", "0"} {
		if isTrue(s) {
			t.Errorf("isTrue(%q) = true, want false", s)
		}
	}
}

func TestU128Less(t *testing.T) {
	a, b, c := u128{1, 0}, u128{1, 1}, u128{2, 0}
	if !a.less(b) || !b.less(c) || !a.less(c) {
		t.Error("expected a < b < c")
	}
	if b.less(a) || a.less(a) || c.less(a) {
		t.Error("unexpected ordering")
	}
}

func TestLow32(t *testing.T) {
	if got := low32(0x0000ffff01020304); got != 0x01020304 {
		t.Errorf("got %#x", got)
	}
	if got := low32(0x1ffffffff); got != 0xffffffff {
		t.Errorf("got %#x", got)
	}
}

func TestParseU128(t *testing.T) {
	mask := new(big.Int).SetUint64(^uint64(0))
	tests := []struct {
		in      string
		want    u128
		wantErr bool
	}{
		{"0", u128{0, 0}, false},
		{"4294967295", u128{0, 4294967295}, false},
		{"18446744073709551615", u128{0, ^uint64(0)}, false},
		{"18446744073709551616", u128{1, 0}, false},
		{"340282366920938463463374607431768211455", u128{^uint64(0), ^uint64(0)}, false},
		{"", u128{}, true},
		{"abc", u128{}, true},
		{"1234567890123456789012x", u128{}, true},
	}
	for _, tc := range tests {
		got, err := parseU128(tc.in, mask)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseU128(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseU128(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Database loading and lookup
// ---------------------------------------------------------------------------

func TestLoadDatabase(t *testing.T) {
	db, err := loadDatabase(testDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.from4) != 5 || len(db.to4) != 5 || len(db.cc4) != 5 {
		t.Errorf("IPv4 ranges: got %d/%d/%d, want 5", len(db.from4), len(db.to4), len(db.cc4))
	}
	if len(db.from6) != 2 || len(db.to6) != 2 || len(db.cc6) != 2 {
		t.Errorf("IPv6 ranges: got %d/%d/%d, want 2", len(db.from6), len(db.to6), len(db.cc6))
	}
}

func TestLoadDatabaseUnsortedInput(t *testing.T) {
	rows := standardRows()
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	db, err := loadDatabase(writeCSV(t, t.TempDir(), "unsorted.csv", rows))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(db.from4); i++ {
		if db.from4[i-1] > db.from4[i] {
			t.Fatal("IPv4 ranges are not sorted")
		}
	}
	for i := 1; i < len(db.from6); i++ {
		if db.from6[i].less(db.from6[i-1]) {
			t.Fatal("IPv6 ranges are not sorted")
		}
	}
	for ip, want := range map[string]string{"1.0.0.1": "AU", "80.1.1.1": "DE", "2001:db8::5": "FR", "2400::9": "CN"} {
		if got := db.lookup(net.ParseIP(ip)); got != want {
			t.Errorf("lookup(%s) = %q, want %q", ip, got, want)
		}
	}
}

func TestLoadDatabaseSkipsInvalidRows(t *testing.T) {
	content := "\"ip_from\",\"ip_to\",\"country_code\",\"country_name\"\n" +
		"\"1\",\"2\"\n" +
		"\"x\",\"y\",\"DE\",\"Germany\"\n" +
		fmt.Sprintf("\"%s\",\"%s\",\"de\",\"Germany\"\n", num(t, "80.0.0.0"), num(t, "80.0.0.255"))
	path := filepath.Join(t.TempDir(), "mixed.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := loadDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.from4) != 1 || len(db.from6) != 0 {
		t.Fatalf("got %d IPv4 / %d IPv6 ranges, want 1 / 0", len(db.from4), len(db.from6))
	}
	if db.cc4[0] != "DE" {
		t.Errorf("country code not upper-cased: %q", db.cc4[0])
	}
}

func TestLoadDatabaseErrors(t *testing.T) {
	if _, err := loadDatabase(filepath.Join(t.TempDir(), "missing.csv")); err == nil {
		t.Error("expected error for missing file")
	}
	path := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(path, []byte("1,2\"x,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDatabase(path); err == nil {
		t.Error("expected error for malformed CSV")
	}
}

func TestLookup(t *testing.T) {
	db, err := loadDatabase(testDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ ip, want string }{
		{"1.0.0.0", "AU"},
		{"1.0.0.255", "AU"},
		{"1.0.1.0", ""},
		{"0.0.0.1", ""},
		{"5.1.2.3", "RU"},
		{"8.8.8.8", "US"},
		{"80.255.255.255", "DE"},
		{"81.0.0.0", ""},
		{"90.0.0.5", "-"},
		{"255.255.255.255", ""},
		{"::ffff:8.8.8.8", "US"},
		{"2001:db8::1", "FR"},
		{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "FR"},
		{"2001:db8:1::", "FR"},
		{"2001:db9::", ""},
		{"2001:db7:ffff::", ""},
		{"2400::1", "CN"},
		{"::1", ""},
		{"ff02::1", ""},
	}
	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("invalid test IP %q", tc.ip)
		}
		if got := db.lookup(ip); got != tc.want {
			t.Errorf("lookup(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

func TestLookupEdgeCases(t *testing.T) {
	if got := (&database{}).lookup(net.ParseIP("1.1.1.1")); got != "" {
		t.Errorf("empty database: got %q", got)
	}
	if got := (&database{}).lookup(net.ParseIP("2001:db8::1")); got != "" {
		t.Errorf("empty database (IPv6): got %q", got)
	}
	db, err := loadDatabase(testDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.lookup(net.IP{1, 2, 3}); got != "" {
		t.Errorf("invalid IP: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Shared database cache
// ---------------------------------------------------------------------------

func TestGetDatabaseCachesAndReloads(t *testing.T) {
	path := testDBPath(t)

	a, err := getDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := getDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("expected the same shared instance for an unchanged file")
	}

	// Replace the file with different content (different size).
	rows := standardRows()
	rows = append(rows, row{"100.0.0.0", "100.0.0.255", "JP"})
	writeCSV(t, filepath.Dir(path), filepath.Base(path), rows)

	c, err := getDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("expected a new instance after the file changed")
	}
	if got := c.lookup(net.ParseIP("100.0.0.1")); got != "JP" {
		t.Errorf("new database not used: got %q", got)
	}

	dbCacheMu.Lock()
	entries := 0
	for k := range dbCache {
		if strings.HasPrefix(k, path+"|") {
			entries++
		}
	}
	dbCacheMu.Unlock()
	if entries != 1 {
		t.Errorf("expected exactly 1 cache entry for the file, got %d", entries)
	}
}

func TestGetDatabaseErrors(t *testing.T) {
	if _, err := getDatabase(filepath.Join(t.TempDir(), "missing.csv")); err == nil {
		t.Error("expected error for missing file")
	}
	path := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(path, []byte("1,2\"x,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := getDatabase(path); err == nil {
		t.Error("expected error for malformed CSV")
	}
}

func TestGetDatabaseConcurrent(t *testing.T) {
	path := testDBPath(t)
	const workers = 16
	results := make([]*database, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = getDatabase(path)
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("worker %d got a different instance", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware construction
// ---------------------------------------------------------------------------

func TestNewErrors(t *testing.T) {
	path := testDBPath(t)
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"empty path", func(c *Config) { c.DatabaseFilePath = "" }, "databaseFilePath"},
		{"missing file", func(c *Config) { c.DatabaseFilePath = filepath.Join(t.TempDir(), "nope.csv") }, "could not load database"},
		{"invalid mode", func(c *Config) { c.Mode = "maybe" }, "invalid mode"},
		{"invalid country", func(c *Config) { c.Countries = "RUS" }, "invalid country code"},
		{"allow without countries", func(c *Config) { c.Mode, c.Countries = "allow", "" }, "without countries"},
		{"invalid allowedIPs", func(c *Config) { c.AllowedIPs = "not-an-ip" }, "invalid allowedIPs"},
		{"status not a number", func(c *Config) { c.StatusCode = "abc" }, "invalid statusCode"},
		{"status too low", func(c *Config) { c.StatusCode = "99" }, "invalid statusCode"},
		{"status too high", func(c *Config) { c.StatusCode = "600" }, "invalid statusCode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig(path)
			tc.mutate(cfg)
			_, err := New(context.Background(), http.NotFoundHandler(), cfg, "test")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewAcceptsModeAliases(t *testing.T) {
	path := testDBPath(t)
	for _, mode := range []string{"", "block", "BLOCK", " deny ", "blacklist", "allow", "Allow", "whitelist"} {
		cfg := baseConfig(path)
		cfg.Countries = "DE"
		cfg.Mode = mode
		h, err := New(context.Background(), http.NotFoundHandler(), cfg, "test")
		if err != nil || h == nil {
			t.Errorf("mode %q: unexpected result (%v, %v)", mode, h, err)
		}
	}
}

func TestNewBlockModeWithoutCountries(t *testing.T) {
	cfg := baseConfig(testDBPath(t))
	cfg.Countries = ""
	if _, err := New(context.Background(), http.NotFoundHandler(), cfg, "test"); err != nil {
		t.Fatalf("block mode without countries should be valid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Request handling
// ---------------------------------------------------------------------------

func TestServeHTTP(t *testing.T) {
	path := testDBPath(t)

	allowDE := func(c *Config) {
		c.Mode = "allow"
		c.Countries = "DE,AT"
	}
	allowDEStrict := func(c *Config) {
		allowDE(c)
		c.BlockUnknown = "true"
	}
	allowDEPlus := func(allowed string) func(*Config) {
		return func(c *Config) {
			allowDE(c)
			c.AllowedIPs = allowed
		}
	}
	countries := func(cc string) func(*Config) {
		return func(c *Config) { c.Countries = cc }
	}
	strictPrivate := func(c *Config) {
		c.AllowPrivate = "false"
		c.BlockUnknown = "true"
	}
	privateOff := func(c *Config) { c.AllowPrivate = "false" }
	blockUnknown := func(c *Config) { c.BlockUnknown = "true" }
	trustXFF := func(c *Config) { c.TrustForwardedHeader = "true" }
	status451 := func(c *Config) { c.StatusCode = "451" }
	emptyStatus := func(c *Config) { c.StatusCode = "" }
	xff := func(v string) map[string]string { return map[string]string{"X-Forwarded-For": v} }

	tests := []struct {
		name     string
		mutate   func(*Config)
		remote   string
		header   map[string]string
		wantCode int
	}{
		{"block mode denies listed country", nil, "5.1.1.1:1234", nil, deniedCode},
		{"block mode allows other country", nil, "8.8.8.8:1234", nil, okCode},
		{"IPv4-mapped IPv6 client", nil, "[::ffff:5.1.1.1]:1234", nil, deniedCode},
		{"IPv6 denied", countries("FR"), "[2001:db8::1]:443", nil, deniedCode},
		{"IPv6 allowed", countries("FR"), "[2400::1]:443", nil, okCode},

		{"allow mode permits listed country", allowDE, "80.1.1.1:1", nil, okCode},
		{"allow mode denies other country", allowDE, "8.8.8.8:1", nil, deniedCode},

		{"allowedIPs single IP", allowDEPlus("8.8.8.8"), "8.8.8.8:1", nil, okCode},
		{"allowedIPs CIDR match", allowDEPlus("8.8.8.0/25"), "8.8.8.99:1", nil, okCode},
		{"allowedIPs CIDR no match", allowDEPlus("8.8.8.0/25"), "8.8.8.200:1", nil, deniedCode},
		{"allowedIPs IPv6 match", allowDEPlus("2001:db8::1"), "[2001:db8::1]:1", nil, okCode},
		{"allowedIPs IPv6 no match", allowDEPlus("2001:db8::1"), "[2001:db8::2]:1", nil, deniedCode},

		{"private IPv4 passes", allowDE, "192.168.1.10:1", nil, okCode},
		{"private IPv6 passes", allowDE, "[fd00::1]:1", nil, okCode},
		{"loopback IPv4 passes", allowDE, "127.0.0.1:1", nil, okCode},
		{"loopback IPv6 passes", allowDE, "[::1]:1", nil, okCode},
		{"link-local passes", allowDE, "169.254.1.1:1", nil, okCode},
		{"private denied when disabled and unknown blocked", strictPrivate, "192.168.1.10:1", nil, deniedCode},
		{"private is unknown and passes when only disabled", privateOff, "192.168.1.10:1", nil, okCode},

		{"unknown range passes by default", nil, "90.0.0.5:1", nil, okCode},
		{"unknown range blocked with blockUnknown", blockUnknown, "90.0.0.5:1", nil, deniedCode},
		{"IP not in database passes by default", nil, "100.0.0.1:1", nil, okCode},
		{"IP not in database blocked with blockUnknown", blockUnknown, "100.0.0.1:1", nil, deniedCode},
		{"allow mode lets unknown IPs pass by default", allowDE, "100.0.0.1:1", nil, okCode},
		{"allow mode blocks unknown IPs with blockUnknown", allowDEStrict, "100.0.0.1:1", nil, deniedCode},

		{"X-Forwarded-For ignored by default", nil, "8.8.8.8:1", xff("5.1.1.1"), okCode},
		{"X-Forwarded-For trusted", trustXFF, "8.8.8.8:1", xff("5.1.1.1"), deniedCode},
		{"X-Forwarded-For uses first entry (denied)", trustXFF, "8.8.8.8:1", xff("5.1.1.1, 8.8.8.8"), deniedCode},
		{"X-Forwarded-For uses first entry (allowed)", trustXFF, "5.1.1.1:1", xff("8.8.8.8, 5.1.1.1"), okCode},
		{"invalid X-Forwarded-For falls back to remote", trustXFF, "5.1.1.1:1", xff("garbage"), deniedCode},
		{"missing X-Forwarded-For falls back to remote", trustXFF, "5.1.1.1:1", nil, deniedCode},

		{"custom status code", status451, "5.1.1.1:1", nil, http.StatusUnavailableForLegalReasons},
		{"empty status code keeps default", emptyStatus, "5.1.1.1:1", nil, deniedCode},

		{"remote address without port", nil, "5.1.1.1", nil, deniedCode},
		{"unparsable remote address", nil, "garbage", nil, deniedCode},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig(path)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			res := serve(t, cfg, tc.remote, tc.header)
			if res.code != tc.wantCode {
				t.Errorf("status = %d, want %d", res.code, tc.wantCode)
			}
			if res.nextCalled != (tc.wantCode == okCode) {
				t.Errorf("next handler called = %v, status %d", res.nextCalled, res.code)
			}
		})
	}
}

func TestBlockedResponseBody(t *testing.T) {
	res := serve(t, baseConfig(testDBPath(t)), "5.1.1.1:1", nil)
	if res.code != deniedCode {
		t.Fatalf("status = %d, want %d", res.code, deniedCode)
	}
	if strings.TrimSpace(res.body) != http.StatusText(deniedCode) {
		t.Errorf("body = %q", res.body)
	}
}
