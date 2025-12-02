# LBO Router - DNS Snooper Query Tool

This tool queries the snooper plugin's PostgreSQL database to retrieve IP addresses matching wildcard FQDN patterns, useful for implementing Local Breakout (LBO) routing.

## Build

```bash
go build -o lbo-router .
```

## Usage

### Basic Usage - Get IPs for a Pattern

```bash
# Get all IPs for google.com subdomains
./lbo-router -pattern "%.google.com"

# Get all IPs for multiple patterns
./lbo-router -patterns "%.google.com,%.youtube.com,%.googleapis.com"
```

### With Custom Database Connection

```bash
./lbo-router \
  -dsn "host=db.example.com port=5432 user=myuser password=mypass dbname=dns sslmode=require" \
  -pattern "%.microsoft.com"
```

### With Age Filter

Only return records seen within the last 30 minutes:

```bash
./lbo-router -pattern "%.office365.com" -max-age 30m
```

### Verbose Mode - Show Detailed Records

```bash
./lbo-router -pattern "%.google.com" -verbose
```

Output:
```
Pattern: %.google.com (15 records)
  www.google.com                           -> 142.250.196.68  (TTL:   300, count: 42, last: 2024-01-15 10:30:15)
  mail.google.com                          -> 142.250.196.69  (TTL:   300, count: 28, last: 2024-01-15 10:28:45)
  ...
```

### Generate Route Commands

```bash
./lbo-router -pattern "%.zoom.us" -gateway "10.0.0.1"
```

Output:
```
Found 8 unique IPs:
3.7.35.0
3.21.137.0
...
------------------------------------------------------------
Route commands:
ip route add 3.7.35.0/32 via 10.0.0.1
ip route add 3.21.137.0/32 via 10.0.0.1
...
```

## Pattern Syntax

The pattern uses SQL LIKE syntax:
- `%` matches any sequence of characters
- `_` matches any single character

Examples:
| Pattern | Matches |
|---------|---------|
| `%.google.com` | All subdomains of google.com |
| `www.%` | All domains starting with www. |
| `%.cdn.%` | Any FQDN containing .cdn. |
| `%zoom%` | Any FQDN containing "zoom" |

## Integration Example

### Periodic Route Update Script

```bash
#!/bin/bash
# update-routes.sh - Run periodically via cron

GATEWAY="10.0.0.1"
PATTERNS="%.zoom.us,%.teams.microsoft.com,%.webex.com"

# Get current IPs
CURRENT_IPS=$(./lbo-router -patterns "$PATTERNS" -max-age 1h 2>/dev/null | grep -E '^[0-9]+\.')

# Apply routes
for ip in $CURRENT_IPS; do
    ip route replace $ip/32 via $GATEWAY 2>/dev/null
done
```

### As a Go Library

```go
package main

import (
    "fmt"
    "time"
)

func main() {
    db, _ := NewSnooperDB("host=localhost dbname=coredns sslmode=disable")
    defer db.Close()

    // Get IPs for SaaS services
    patterns := []string{
        "%.salesforce.com",
        "%.office365.com",
        "%.zoom.us",
    }

    ips, _ := db.GetIPsByPatterns(patterns, 1*time.Hour)

    // Update routing table
    for _, ip := range ips {
        updateRoute(ip, "10.0.0.1")
    }
}
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-dsn` | `host=localhost port=5432 user=coredns dbname=coredns sslmode=disable` | PostgreSQL connection string |
| `-pattern` | - | Single FQDN pattern (SQL LIKE syntax) |
| `-patterns` | - | Comma-separated list of patterns |
| `-max-age` | `1h` | Only return records seen within this duration |
| `-gateway` | - | Gateway IP for generated route commands |
| `-verbose` | `false` | Show detailed record information |
