# LBO Backend - Real-time DNS Update Listener

This sample demonstrates how to receive real-time notifications when the snooper plugin detects DNS records matching LBO patterns.

## Architecture

```
┌─────────────┐     DNS Query         ┌─────────────┐
│   Client    │ ──────────────────▶   │   CoreDNS   │
└─────────────┘                       │  (snooper)  │
                                      └──────┬──────┘
                                             │ INSERT/UPDATE
                                             ▼
┌─────────────┐     NOTIFY            ┌─────────────┐
│ LBO Backend │ ◀─────────────────    │  PostgreSQL │
│ (listener)  │   lbo_update          │  + Trigger  │
└──────┬──────┘                       └─────────────┘
       │
       │ gRPC Stream
       ▼
┌─────────────┐
│  LBO Agent  │ ──▶ ip route add
│   (端末)    │
└─────────────┘
```

## Setup

### 1. Apply Database Schema

```bash
psql -h localhost -U coredns -d coredns -f schema.sql
```

This creates:
- `lbo_patterns` - LBO対象のFQDNパターンを管理
- `lbo_notifications` - 通知ログ（デバッグ用）
- PostgreSQL Trigger - dns_records更新時にパターンマッチ→NOTIFY

### 2. Add LBO Patterns

```sql
-- Zoom
INSERT INTO lbo_patterns (pattern, gateway, description, priority)
VALUES ('%.zoom.us', '10.0.0.1', 'Zoom video conferencing', 10);

INSERT INTO lbo_patterns (pattern, gateway, description, priority)
VALUES ('%.zoom.com', '10.0.0.1', 'Zoom video conferencing', 10);

-- Microsoft Teams
INSERT INTO lbo_patterns (pattern, gateway, description, priority)
VALUES ('%.teams.microsoft.com', '10.0.0.1', 'Microsoft Teams', 20);

-- Office 365
INSERT INTO lbo_patterns (pattern, gateway, description, priority)
VALUES ('%.office365.com', '10.0.0.1', 'Microsoft 365', 30);
```

### 3. Run the Listener

```bash
go build -o lbo-backend .
./lbo-backend -dsn "host=localhost port=5432 user=coredns dbname=coredns sslmode=disable"
```

## How It Works

### PostgreSQL NOTIFY/LISTEN

1. **Trigger fires** - When snooper INSERTs/UPDATEs `dns_records`
2. **Pattern match** - Trigger checks if FQDN matches any `lbo_patterns`
3. **NOTIFY sent** - If matched, sends JSON payload to `lbo_update` channel
4. **Backend receives** - Go listener receives notification in real-time

### Notification Payload

```json
{
  "event": "INSERT",
  "fqdn": "us02web.zoom.us",
  "ip": "3.7.35.0",
  "ttl": 300,
  "pattern_id": 1,
  "pattern": "%.zoom.us",
  "gateway": "10.0.0.1",
  "timestamp": "2024-01-15T10:30:00Z"
}
```

## Integration with Your gRPC Backend

```go
// In your existing gRPC server
func (s *Server) StartLBOListener(dsn string) {
    listener := NewLBOListener(dsn)

    listener.OnUpdate(func(update *LBOUpdate) {
        // Forward to connected agents via gRPC stream
        s.BroadcastToAgents(&pb.LBOUpdate{
            Fqdn:    update.FQDN,
            Ip:      update.IP,
            Gateway: update.Gateway,
            Ttl:     int32(update.TTL),
        })
    })

    listener.Start()
}
```

## Testing

### Simulate DNS Query

```bash
# Query a domain that matches an LBO pattern
dig @localhost zoom.us
```

### Check Notifications in psql

```sql
-- Watch notifications in real-time
LISTEN lbo_update;

-- Check notification log
SELECT * FROM lbo_notifications ORDER BY notified_at DESC LIMIT 10;
```

### Verify Pattern Matching

```sql
-- See active LBO mappings
SELECT * FROM lbo_active_mappings;

-- Pattern statistics
SELECT * FROM lbo_pattern_stats;
```

## Managing Patterns

### Enable/Disable Pattern

```sql
UPDATE lbo_patterns SET enabled = false WHERE pattern = '%.zoom.us';
```

### Change Gateway

```sql
UPDATE lbo_patterns SET gateway = '10.0.0.2' WHERE pattern LIKE '%.microsoft%';
```

### Delete Pattern

```sql
DELETE FROM lbo_patterns WHERE pattern = '%.zoom.us';
```

Pattern changes also trigger notifications on `lbo_pattern_change` channel.

## Performance Considerations

1. **Index optimization** - The trigger uses pattern matching which benefits from:
   ```sql
   CREATE INDEX idx_dns_records_fqdn_trgm ON dns_records USING gin (fqdn gin_trgm_ops);
   ```

2. **Notification batching** - For high-volume scenarios, consider:
   - Batching notifications in the trigger
   - Using a message queue (Redis, NATS) instead of NOTIFY

3. **Connection pooling** - Use connection pool for the listener:
   ```go
   db.SetMaxOpenConns(5)
   db.SetMaxIdleConns(2)
   ```

## Files

| File | Description |
|------|-------------|
| `schema.sql` | PostgreSQL schema with tables, triggers, and views |
| `listener.go` | Go sample for receiving NOTIFY and forwarding to agents |
| `README.md` | This documentation |
