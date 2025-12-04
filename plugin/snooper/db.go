package snooper

import (
	"database/sql"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// DNSRecord represents a DNS A record mapping
type DNSRecord struct {
	ID           int64
	FQDN         string
	FQDNReversed string
	IP           string
	TTL          uint32
	FirstSeen    time.Time
	LastSeen     time.Time
	Count        int64
}

// DB wraps the PostgreSQL database connection
type DB struct {
	db *sql.DB
	mu sync.Mutex
}

// NewDB creates a new database connection and initializes the schema
// connStr should be a PostgreSQL connection string like:
// "host=localhost port=5432 user=coredns password=secret dbname=coredns sslmode=disable"
func NewDB(connStr string) (*DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	// Set connection pool settings for PostgreSQL
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	d := &DB{db: db}
	if err := d.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return d, nil
}

// initSchema creates the necessary tables if they don't exist
func (d *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS dns_records (
		id SERIAL PRIMARY KEY,
		fqdn TEXT NOT NULL,
		fqdn_reversed TEXT NOT NULL,
		ip INET NOT NULL,
		ttl INTEGER NOT NULL DEFAULT 0,
		first_seen TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		last_seen TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		count BIGINT NOT NULL DEFAULT 1,
		UNIQUE(fqdn, ip)
	);

	-- Index on reversed FQDN for efficient prefix matching (suffix search on original)
	-- Pattern '%.google.com' becomes 'com.google.%' which uses this index
	CREATE INDEX IF NOT EXISTS idx_dns_records_fqdn_reversed ON dns_records(fqdn_reversed text_pattern_ops);

	-- Keep original FQDN index for exact match queries
	CREATE INDEX IF NOT EXISTS idx_dns_records_fqdn ON dns_records(fqdn);
	CREATE INDEX IF NOT EXISTS idx_dns_records_ip ON dns_records(ip);
	CREATE INDEX IF NOT EXISTS idx_dns_records_last_seen ON dns_records(last_seen);
	`

	_, err := d.db.Exec(schema)
	return err
}

// ReverseFQDN reverses the labels of an FQDN for efficient suffix matching
// e.g., "www.google.com" -> "com.google.www"
func ReverseFQDN(fqdn string) string {
	parts := strings.Split(fqdn, ".")
	// Reverse the slice
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

// ReversePattern converts a suffix pattern to a prefix pattern for reversed FQDN
// e.g., "%.google.com" -> "com.google.%"
func ReversePattern(pattern string) string {
	// Handle the wildcard at the beginning
	hasPrefix := strings.HasPrefix(pattern, "%")
	hasSuffix := strings.HasSuffix(pattern, "%")

	// Remove wildcards temporarily
	p := strings.TrimPrefix(pattern, "%")
	p = strings.TrimSuffix(p, "%")
	p = strings.Trim(p, ".")

	// Reverse the domain parts
	reversed := ReverseFQDN(p)

	// Re-add wildcards in reversed positions
	if hasPrefix {
		reversed = reversed + ".%"
	}
	if hasSuffix {
		reversed = "%." + reversed
	}

	return reversed
}

// Upsert inserts or updates a DNS record
func (d *DB) Upsert(fqdn, ip string, ttl uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	fqdnReversed := ReverseFQDN(fqdn)

	query := `
	INSERT INTO dns_records (fqdn, fqdn_reversed, ip, ttl, first_seen, last_seen, count)
	VALUES ($1, $2, $3, $4, NOW(), NOW(), 1)
	ON CONFLICT(fqdn, ip) DO UPDATE SET
		ttl = EXCLUDED.ttl,
		last_seen = NOW(),
		count = dns_records.count + 1
	`

	_, err := d.db.Exec(query, fqdn, fqdnReversed, ip, ttl)
	return err
}

// UpsertBatch inserts or updates multiple DNS records in a single transaction
func (d *DB) UpsertBatch(records []DNSRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO dns_records (fqdn, fqdn_reversed, ip, ttl, first_seen, last_seen, count)
		VALUES ($1, $2, $3, $4, NOW(), NOW(), 1)
		ON CONFLICT(fqdn, ip) DO UPDATE SET
			ttl = EXCLUDED.ttl,
			last_seen = NOW(),
			count = dns_records.count + 1
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		fqdnReversed := ReverseFQDN(r.FQDN)
		if _, err := stmt.Exec(r.FQDN, fqdnReversed, r.IP, r.TTL); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetByFQDN returns all records matching the given FQDN
func (d *DB) GetByFQDN(fqdn string) ([]DNSRecord, error) {
	rows, err := d.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE fqdn = $1
		ORDER BY last_seen DESC
	`, fqdn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetByIP returns all records matching the given IP
func (d *DB) GetByIP(ip string) ([]DNSRecord, error) {
	rows, err := d.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE ip = $1::INET
		ORDER BY last_seen DESC
	`, ip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetByFQDNPattern returns all records matching the given FQDN pattern (SQL LIKE)
// Use % as wildcard, e.g., "%.google.com" for all google.com subdomains
// The pattern is automatically converted to use the reversed FQDN index for efficiency
func (d *DB) GetByFQDNPattern(pattern string) ([]DNSRecord, error) {
	reversedPattern := ReversePattern(pattern)

	rows, err := d.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE fqdn_reversed LIKE $1
		ORDER BY last_seen DESC
	`, reversedPattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetActiveByFQDNPattern returns records matching pattern that were seen within maxAge
func (d *DB) GetActiveByFQDNPattern(pattern string, maxAge time.Duration) ([]DNSRecord, error) {
	reversedPattern := ReversePattern(pattern)
	cutoff := time.Now().Add(-maxAge)

	rows, err := d.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		WHERE fqdn_reversed LIKE $1 AND last_seen > $2
		ORDER BY last_seen DESC
	`, reversedPattern, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetAll returns all records
func (d *DB) GetAll() ([]DNSRecord, error) {
	rows, err := d.db.Query(`
		SELECT id, fqdn, fqdn_reversed, ip::TEXT, ttl, first_seen, last_seen, count
		FROM dns_records
		ORDER BY last_seen DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// CleanupExpired removes records that haven't been seen since the given duration
func (d *DB) CleanupExpired(maxAge time.Duration) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	result, err := d.db.Exec(`
		DELETE FROM dns_records
		WHERE last_seen < $1
	`, cutoff)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// Close closes the database connection
func (d *DB) Close() error {
	return d.db.Close()
}

func scanRecords(rows *sql.Rows) ([]DNSRecord, error) {
	var records []DNSRecord
	for rows.Next() {
		var r DNSRecord
		if err := rows.Scan(&r.ID, &r.FQDN, &r.FQDNReversed, &r.IP, &r.TTL, &r.FirstSeen, &r.LastSeen, &r.Count); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
