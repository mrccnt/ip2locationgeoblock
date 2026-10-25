// SPDX-License-Identifier: Apache-2.0

// Package ip2locationgeoblock is a Traefik middleware plugin that blocks or
// allows requests based on the client's country code, using the IP2Location
// LITE CSV database (DB1).
//
// This product includes IP2Location LITE data available from
// https://lite.ip2location.com.
package ip2locationgeoblock

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Config is the plugin configuration.
// All values are deliberately strings so that they can be set easily via
// Docker labels without list syntax or type conversion issues.
type Config struct {
	// DatabaseFilePath is the path to the IP2Location LITE DB1 CSV file.
	DatabaseFilePath string `json:"databaseFilePath,omitempty"`
	// Countries is a comma-separated list of ISO 3166-1 alpha-2 codes, e.g. "RU,CN,KP".
	Countries string `json:"countries,omitempty"`
	// Mode is "block" (default): listed countries are denied.
	// "allow": only listed countries are permitted.
	Mode string `json:"mode,omitempty"`
	// AllowedIPs is a comma-separated list of IPs/CIDRs that always pass.
	AllowedIPs string `json:"allowedIPs,omitempty"`
	// AllowPrivate ("true" by default): private/loopback addresses always pass.
	AllowPrivate string `json:"allowPrivate,omitempty"`
	// BlockUnknown ("false" by default): deny IPs without a country mapping.
	BlockUnknown string `json:"blockUnknown,omitempty"`
	// TrustForwardedHeader: use the client IP from X-Forwarded-For.
	// Only enable this if a trusted proxy sits in front of Traefik!
	TrustForwardedHeader string `json:"trustForwardedHeader,omitempty"`
	// StatusCode is the HTTP status returned for blocked requests (default 403).
	StatusCode string `json:"statusCode,omitempty"`
	// AllowMissingDatabase ("false" by default): if "true" and the database file
	// does not exist, the middleware still starts, but passes ALL requests
	// through unfiltered. This exists only for the Traefik Plugin Catalog
	// validity test (testData); do not enable it in production.
	AllowMissingDatabase string `json:"allowMissingDatabase,omitempty"`
}

// CreateConfig creates the default configuration.
func CreateConfig() *Config {
	return &Config{
		Mode:                 "block",
		AllowPrivate:         "true",
		BlockUnknown:         "false",
		StatusCode:           "403",
		AllowMissingDatabase: "false",
	}
}

// ---------------------------------------------------------------------------
// Database
// ---------------------------------------------------------------------------

type u128 struct{ hi, lo uint64 }

func (a u128) less(b u128) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}

type database struct {
	from4 []uint32
	to4   []uint32
	cc4   []string

	from6 []u128
	to6   []u128
	cc6   []string
}

const (
	v4MappedBase = 0xffff00000000 // ::ffff:0.0.0.0
	v4MappedMax  = 0xffffffffffff // ::ffff:255.255.255.255
)

// parseU128 parses a decimal number of up to 128 bits.
func parseU128(s string, mask *big.Int) (u128, error) {
	if len(s) <= 19 {
		v, err := strconv.ParseUint(s, 10, 64)
		return u128{0, v}, err
	}
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return u128{}, fmt.Errorf("invalid number %q", s)
	}
	hi := new(big.Int).Rsh(b, 64).Uint64()
	lo := new(big.Int).And(b, mask).Uint64()
	return u128{hi, lo}, nil
}

// low32 returns the lower 32 bits of v. Callers guarantee that the value fits
// into 32 bits; reading via encoding/binary avoids an integer conversion.
func low32(v uint64) uint32 {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return binary.BigEndian.Uint32(b[4:])
}

func loadDatabase(path string) (*database, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true

	db := &database{}
	intern := map[string]string{}
	mask := new(big.Int).SetUint64(^uint64(0))

	for {
		rec, readErr := r.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(rec) < 3 {
			continue
		}
		from, err1 := parseU128(rec[0], mask)
		to, err2 := parseU128(rec[1], mask)
		if err1 != nil || err2 != nil {
			continue // header line or malformed row
		}

		// Intern country codes so that all rows share one string per country.
		cc := strings.ToUpper(rec[2])
		if v, ok := intern[cc]; ok {
			cc = v
		} else {
			intern[cc] = cc
		}

		switch {
		case from.hi == 0 && to.hi == 0 && to.lo <= 0xFFFFFFFF:
			db.from4 = append(db.from4, low32(from.lo))
			db.to4 = append(db.to4, low32(to.lo))
			db.cc4 = append(db.cc4, cc)
		case from.hi == 0 && to.hi == 0 && from.lo >= v4MappedBase && to.lo <= v4MappedMax:
			db.from4 = append(db.from4, low32(from.lo-v4MappedBase))
			db.to4 = append(db.to4, low32(to.lo-v4MappedBase))
			db.cc4 = append(db.cc4, cc)
		default:
			db.from6 = append(db.from6, from)
			db.to6 = append(db.to6, to)
			db.cc6 = append(db.cc6, cc)
		}
	}

	db.sortIPv4()
	db.sortIPv6()

	log.Printf("[ip2locationgeoblock] loaded %s: %d IPv4 and %d IPv6 ranges", path, len(db.from4), len(db.from6))
	return db, nil
}

// sortIPv4 sorts the IPv4 ranges, in case the file is not sorted.
func (d *database) sortIPv4() {
	if sort.SliceIsSorted(d.from4, func(i, j int) bool { return d.from4[i] < d.from4[j] }) {
		return
	}
	idx := make([]int, len(d.from4))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return d.from4[idx[a]] < d.from4[idx[b]] })
	f4, t4, c4 := make([]uint32, len(idx)), make([]uint32, len(idx)), make([]string, len(idx))
	for i, j := range idx {
		f4[i], t4[i], c4[i] = d.from4[j], d.to4[j], d.cc4[j]
	}
	d.from4, d.to4, d.cc4 = f4, t4, c4
}

// sortIPv6 sorts the IPv6 ranges, in case the file is not sorted.
func (d *database) sortIPv6() {
	if sort.SliceIsSorted(d.from6, func(i, j int) bool { return d.from6[i].less(d.from6[j]) }) {
		return
	}
	idx := make([]int, len(d.from6))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return d.from6[idx[a]].less(d.from6[idx[b]]) })
	f6, t6, c6 := make([]u128, len(idx)), make([]u128, len(idx)), make([]string, len(idx))
	for i, j := range idx {
		f6[i], t6[i], c6[i] = d.from6[j], d.to6[j], d.cc6[j]
	}
	d.from6, d.to6, d.cc6 = f6, t6, c6
}

// lookup returns the country code, or "" if nothing was found.
func (d *database) lookup(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		n := binary.BigEndian.Uint32(v4)
		i := sort.Search(len(d.from4), func(i int) bool { return d.from4[i] > n }) - 1
		if i >= 0 && n <= d.to4[i] {
			return d.cc4[i]
		}
		return ""
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	n := u128{binary.BigEndian.Uint64(v6[:8]), binary.BigEndian.Uint64(v6[8:])}
	i := sort.Search(len(d.from6), func(i int) bool { return n.less(d.from6[i]) }) - 1
	if i >= 0 && !d.to6[i].less(n) {
		return d.cc6[i]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Shared database cache
// ---------------------------------------------------------------------------

var (
	dbCacheMu sync.Mutex
	dbCache   = map[string]*database{}
)

// getDatabase returns one shared database instance per file path, modification
// time and size. Traefik instantiates a middleware once per router and on every
// dynamic configuration change; the cache avoids loading the CSV each time.
// A replaced database file is picked up automatically the next time a
// middleware is (re)built.
func getDatabase(path string) (*database, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.ModTime().UnixNano(), st.Size())

	dbCacheMu.Lock()
	defer dbCacheMu.Unlock()

	if db, ok := dbCache[key]; ok {
		return db, nil
	}
	db, err := loadDatabase(path)
	if err != nil {
		return nil, err
	}
	// Drop stale versions of the same file.
	for k := range dbCache {
		if strings.HasPrefix(k, path+"|") {
			delete(dbCache, k)
		}
	}
	dbCache[key] = db
	return db, nil
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// GeoBlock is the middleware handler.
type GeoBlock struct {
	next         http.Handler
	name         string
	db           *database
	countries    map[string]struct{}
	allowMode    bool
	allowedNets  []*net.IPNet
	allowPrivate bool
	blockUnknown bool
	trustXFF     bool
	status       int
}

func isTrue(s string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(s))
	return b
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// New creates the middleware.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.DatabaseFilePath == "" {
		return nil, errors.New("databaseFilePath must not be empty")
	}
	db, err := getDatabase(config.DatabaseFilePath)
	if err != nil {
		// Only a missing file can be tolerated, and only on explicit request.
		// A malformed database is always an error. On error db is nil, and a
		// middleware without database passes all requests through.
		if !isTrue(config.AllowMissingDatabase) || !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not load database: %w", err)
		}
		log.Printf("[ip2locationgeoblock] WARNING middleware %q: database %q not found, "+
			"allowMissingDatabase=true: requests are NOT filtered", name, config.DatabaseFilePath)
	}

	g := &GeoBlock{
		next:         next,
		name:         name,
		db:           db,
		countries:    map[string]struct{}{},
		allowPrivate: config.AllowPrivate == "" || isTrue(config.AllowPrivate),
		blockUnknown: isTrue(config.BlockUnknown),
		trustXFF:     isTrue(config.TrustForwardedHeader),
		status:       http.StatusForbidden,
	}

	switch strings.ToLower(strings.TrimSpace(config.Mode)) {
	case "", "block", "blacklist", "deny":
		g.allowMode = false
	case "allow", "whitelist":
		g.allowMode = true
	default:
		return nil, fmt.Errorf("invalid mode %q (allowed: block, allow)", config.Mode)
	}

	for _, c := range splitList(config.Countries) {
		c = strings.ToUpper(c)
		if len(c) != 2 {
			return nil, fmt.Errorf("invalid country code %q", c)
		}
		g.countries[c] = struct{}{}
	}
	if len(g.countries) == 0 && g.allowMode {
		return nil, errors.New("mode=allow without countries would block everything")
	}

	for _, s := range splitList(config.AllowedIPs) {
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		_, n, parseErr := net.ParseCIDR(s)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid allowedIPs entry %q: %w", s, parseErr)
		}
		g.allowedNets = append(g.allowedNets, n)
	}

	if config.StatusCode != "" {
		sc, convErr := strconv.Atoi(strings.TrimSpace(config.StatusCode))
		if convErr != nil || sc < 100 || sc > 599 {
			return nil, fmt.Errorf("invalid statusCode %q", config.StatusCode)
		}
		g.status = sc
	}

	log.Printf("[ip2locationgeoblock] middleware %q: mode=%s, countries=%s", name, config.Mode, config.Countries)
	return g, nil
}

func (g *GeoBlock) clientIP(req *http.Request) net.IP {
	if g.trustXFF {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return net.ParseIP(host)
}

func (g *GeoBlock) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if g.db == nil {
		// Only possible with allowMissingDatabase=true (see New).
		g.next.ServeHTTP(rw, req)
		return
	}

	ip := g.clientIP(req)
	if ip == nil {
		log.Printf("[ip2locationgeoblock/%s] could not determine client IP", g.name)
		http.Error(rw, http.StatusText(g.status), g.status)
		return
	}

	if g.allowPrivate && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		g.next.ServeHTTP(rw, req)
		return
	}
	for _, n := range g.allowedNets {
		if n.Contains(ip) {
			g.next.ServeHTTP(rw, req)
			return
		}
	}

	cc := g.db.lookup(ip)
	if cc == "-" {
		cc = ""
	}

	var blocked bool
	if cc == "" {
		blocked = g.blockUnknown
	} else {
		_, listed := g.countries[cc]
		// block mode: listed => blocked; allow mode: not listed => blocked.
		blocked = listed != g.allowMode
	}

	if blocked {
		log.Printf("[ip2locationgeoblock/%s] blocked: %s (%s)", g.name, ip, cc)
		http.Error(rw, http.StatusText(g.status), g.status)
		return
	}
	g.next.ServeHTTP(rw, req)
}
