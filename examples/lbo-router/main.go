// Package main provides a sample application that queries the snooper database
// and retrieves IP addresses matching wildcard FQDN patterns for LBO routing.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Config holds the application configuration
type Config struct {
	DSN      string        // PostgreSQL connection string
	Patterns []string      // FQDN patterns to match (e.g., "%.google.com")
	MaxAge   time.Duration // Only return records seen within this duration
	Gateway  string        // Gateway IP for routing (for display purposes)
}

// DNSRecord represents a record from the snooper database
type DNSRecord struct {
	ID           int64
	FQDN         string
	FQDNReversed string
	IP           string
	TTL          int
	FirstSeen    time.Time
	LastSeen     time.Time
	Count        int64
}

// SnooperDB wraps the database connection
type SnooperDB struct {
	db *sql.DB
}

// NewSnooperDB creates a new database connection
func NewSnooperDB(dsn string) (*SnooperDB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &SnooperDB{db: db}, nil
}

// ReverseFQDN reverses the labels of an FQDN for efficient suffix matching
// e.g., "www.google.com" -> "com.google.www"
func ReverseFQDN(fqdn string) string {
	parts := strings.Split(fqdn, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

// ReversePattern converts a suffix pattern to a prefix pattern for reversed FQDN
// e.g., "%.google.com" -> "com.google.%"
func ReversePattern(pattern string) string {
	hasPrefix := strings.HasPrefix(pattern, "%")
	hasSuffix := strings.HasSuffix(pattern, "%")

	p := strings.TrimPrefix(pattern, "%")
	p = strings.TrimSuffix(p, "%")
	p = strings.Trim(p, ".")

	reversed := ReverseFQDN(p)

	if hasPrefix {
		reversed = reversed + ".%"
	}
	if hasSuffix {
		reversed = "%." + reversed
	}

	return reversed
}

// GetIPsByPattern returns all unique IPs matching the FQDN pattern
// pattern uses SQL LIKE syntax: % = any characters, _ = single character
// Examples:
//   - "%.google.com" - all subdomains of google.com
//   - "www.%" - all domains starting with www.
//   - "%.cdn.%" - any FQDN containing .cdn.
//
// The pattern is automatically converted to use the reversed FQDN index for efficiency
func (s *SnooperDB) GetIPsByPattern(pattern string, maxAge time.Duration) ([]string, error) {
	reversedPattern := ReversePattern(pattern)
	cutoff := time.Now().Add(-maxAge)

	rows, err := s.db.Query(`
		SELECT DISTINCT ip::TEXT
		FROM dns_records
		WHERE fqdn_reversed LIKE $1 AND last_seen > $2
		ORDER BY ip
	`, reversedPattern, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

// GetIPsByPatterns returns all unique IPs matching any of the patterns
func (s *SnooperDB) GetIPsByPatterns(patterns []string, maxAge time.Duration) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	// Build query with multiple LIKE conditions using reversed patterns
	cutoff := time.Now().Add(-maxAge)
	conditions := make([]string, len(patterns))
	args := make([]interface{}, len(patterns)+1)
	for i, p := range patterns {
		conditions[i] = fmt.Sprintf("fqdn_reversed LIKE $%d", i+1)
		args[i] = ReversePattern(p)
	}
	args[len(patterns)] = cutoff

	query := fmt.Sprintf(`
		SELECT DISTINCT ip::TEXT
		FROM dns_records
		WHERE (%s) AND last_seen > $%d
		ORDER BY ip
	`, strings.Join(conditions, " OR "), len(patterns)+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

// GetRecordsByPattern returns full records matching the FQDN pattern
func (s *SnooperDB) GetRecordsByPattern(pattern string, maxAge time.Duration) ([]DNSRecord, error) {
	reversedPattern := ReversePattern(pattern)
	cutoff := time.Now().Add(-maxAge)

	rows, err := s.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE fqdn_reversed LIKE $1 AND last_seen > $2
		ORDER BY last_seen DESC
	`, reversedPattern, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var records []DNSRecord
	for rows.Next() {
		var r DNSRecord
		if err := rows.Scan(&r.ID, &r.FQDN, &r.FQDNReversed, &r.IP, &r.TTL, &r.FirstSeen, &r.LastSeen, &r.Count); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// GetIPsInSubnet returns all IPs from DNS records that fall within the given subnet
func (s *SnooperDB) GetIPsInSubnet(cidr string, maxAge time.Duration) ([]DNSRecord, error) {
	cutoff := time.Now().Add(-maxAge)
	rows, err := s.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE ip << $1::inet AND last_seen > $2
		ORDER BY ip
	`, cidr, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var records []DNSRecord
	for rows.Next() {
		var r DNSRecord
		if err := rows.Scan(&r.ID, &r.FQDN, &r.FQDNReversed, &r.IP, &r.TTL, &r.FirstSeen, &r.LastSeen, &r.Count); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Close closes the database connection
func (s *SnooperDB) Close() error {
	return s.db.Close()
}

// GenerateRouteCommands generates route add commands for the given IPs
func GenerateRouteCommands(ips []string, gateway string) []string {
	var commands []string
	for _, ip := range ips {
		// Validate IP
		if net.ParseIP(ip) == nil {
			continue
		}
		// Generate route command (Linux format)
		cmd := fmt.Sprintf("ip route add %s/32 via %s", ip, gateway)
		commands = append(commands, cmd)
	}
	return commands
}

func main() {
	// Command line flags
	dsn := flag.String("dsn", "host=localhost port=5432 user=coredns dbname=coredns sslmode=disable", "PostgreSQL connection string")
	pattern := flag.String("pattern", "", "FQDN pattern to match (SQL LIKE syntax, e.g., '%.google.com')")
	patterns := flag.String("patterns", "", "Comma-separated list of FQDN patterns")
	maxAge := flag.Duration("max-age", 1*time.Hour, "Maximum age of records to return")
	gateway := flag.String("gateway", "", "Gateway IP for route commands")
	verbose := flag.Bool("verbose", false, "Show detailed record information")
	flag.Parse()

	// Validate input
	if *pattern == "" && *patterns == "" {
		log.Fatal("Either -pattern or -patterns is required")
	}

	// Connect to database
	db, err := NewSnooperDB(*dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Parse patterns
	var patternList []string
	if *pattern != "" {
		patternList = []string{*pattern}
	}
	if *patterns != "" {
		for _, p := range strings.Split(*patterns, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				patternList = append(patternList, p)
			}
		}
	}

	fmt.Printf("Searching for patterns: %v (max age: %v)\n", patternList, *maxAge)
	fmt.Println(strings.Repeat("-", 60))

	if *verbose {
		// Show detailed records
		for _, p := range patternList {
			records, err := db.GetRecordsByPattern(p, *maxAge)
			if err != nil {
				log.Printf("Error querying pattern %s: %v", p, err)
				continue
			}

			fmt.Printf("\nPattern: %s (%d records)\n", p, len(records))
			for _, r := range records {
				fmt.Printf("  %-40s -> %-15s (TTL: %5d, count: %d, last: %s)\n",
					r.FQDN, r.IP, r.TTL, r.Count, r.LastSeen.Format("2006-01-02 15:04:05"))
			}
		}
	} else {
		// Just show unique IPs
		ips, err := db.GetIPsByPatterns(patternList, *maxAge)
		if err != nil {
			log.Fatalf("Error querying patterns: %v", err)
		}

		fmt.Printf("Found %d unique IPs:\n", len(ips))
		for _, ip := range ips {
			fmt.Println(ip)
		}

		// Generate route commands if gateway is specified
		if *gateway != "" {
			fmt.Println(strings.Repeat("-", 60))
			fmt.Println("Route commands:")
			commands := GenerateRouteCommands(ips, *gateway)
			for _, cmd := range commands {
				fmt.Println(cmd)
			}
		}
	}
}
