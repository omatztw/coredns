# snooper

## Name

*snooper* - snoops DNS A record responses and stores FQDN to IP mappings in a PostgreSQL database.

## Description

The *snooper* plugin intercepts DNS responses and extracts A record mappings (FQDN → IP), storing them in a PostgreSQL database. This is useful for implementing Local Breakout (LBO) routing, where you need to know which IPs correspond to specific FQDNs to configure routing tables.

## Syntax

~~~
snooper [ZONES...] {
    dsn CONNECTION_STRING
    cleanup INTERVAL MAX_AGE
}
~~~

* **ZONES** - optional list of zones to snoop. If not specified, all zones are snooped.
* **dsn** - PostgreSQL connection string (default: `host=localhost port=5432 user=coredns dbname=coredns sslmode=disable`)
* **cleanup** - optional cleanup configuration:
  * **INTERVAL** - how often to run cleanup (e.g., `1h`)
  * **MAX_AGE** - delete records not seen within this duration (e.g., `24h`)

## Examples

Snoop all DNS responses with default database:

~~~
. {
    forward . 8.8.8.8
    snooper
}
~~~

Snoop only specific zones with custom database:

~~~
. {
    forward . 8.8.8.8
    snooper example.org example.com {
        dsn "host=db.example.com port=5432 user=coredns password=secret dbname=dns_snooper sslmode=require"
    }
}
~~~

With automatic cleanup of stale records:

~~~
. {
    forward . 8.8.8.8
    snooper {
        dsn "host=localhost port=5432 user=coredns dbname=coredns sslmode=disable"
        cleanup 1h 24h
    }
}
~~~

## Database Schema

The plugin creates a `dns_records` table with the following schema:

| Column | Type | Description |
|--------|------|-------------|
| id | SERIAL | Primary key |
| fqdn | TEXT | Fully qualified domain name (without trailing dot) |
| ip | INET | Resolved IPv4 address (PostgreSQL INET type) |
| ttl | INTEGER | TTL from the DNS response |
| first_seen | TIMESTAMP WITH TIME ZONE | When this mapping was first observed |
| last_seen | TIMESTAMP WITH TIME ZONE | When this mapping was last observed |
| count | BIGINT | Number of times this mapping was observed |

The table has a unique constraint on `(fqdn, ip)` pairs, so each unique mapping is stored once with updated `last_seen` and `count` values.

### Indexes

- `idx_dns_records_fqdn` - for fast FQDN lookups
- `idx_dns_records_ip` - for fast reverse lookups (IP to FQDNs)
- `idx_dns_records_last_seen` - for efficient cleanup queries

## Use Case: Local Breakout (LBO)

This plugin is designed to support Local Breakout scenarios where you need to route traffic for specific FQDNs through different gateways. The workflow is:

1. Configure CoreDNS with the snooper plugin to capture DNS resolutions
2. Query the PostgreSQL database to get IP addresses for FQDNs matching your routing rules
3. Update routing tables to direct traffic for those IPs through the appropriate gateway

### Example Queries

Get all IPs for a specific FQDN:
```sql
SELECT ip FROM dns_records WHERE fqdn = 'www.google.com';
```

Get IPs for a wildcard pattern (all google.com subdomains):
```sql
SELECT DISTINCT ip FROM dns_records
WHERE fqdn LIKE '%.google.com'
AND last_seen > NOW() - INTERVAL '1 hour';
```

Get IPs in a specific subnet:
```sql
SELECT fqdn, ip FROM dns_records
WHERE ip << '10.0.0.0/8'::inet;
```

Get recently active mappings:
```sql
SELECT fqdn, ip, ttl, last_seen, count
FROM dns_records
WHERE last_seen > NOW() - INTERVAL '5 minutes'
ORDER BY last_seen DESC;
```

## PostgreSQL Setup

Create the database and user:

```sql
CREATE USER coredns WITH PASSWORD 'your_password';
CREATE DATABASE coredns OWNER coredns;
GRANT ALL PRIVILEGES ON DATABASE coredns TO coredns;
```

The plugin will automatically create the required table and indexes on startup.

## Metrics

This plugin does not export any metrics currently.

## See Also

* [CoreDNS](https://coredns.io)
* [Local Breakout](https://en.wikipedia.org/wiki/Local_Internet_breakout)
* [PostgreSQL INET Type](https://www.postgresql.org/docs/current/datatype-net-types.html)
