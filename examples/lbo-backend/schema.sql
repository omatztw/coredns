-- LBO (Local Breakout) Pattern Management Schema
-- This extends the snooper plugin's dns_records table with pattern matching
-- and real-time notifications via PostgreSQL NOTIFY.

-- =============================================================================
-- Function: reverse_fqdn
-- Reverses FQDN labels for efficient suffix matching
-- e.g., "www.google.com" -> "com.google.www"
-- =============================================================================
CREATE OR REPLACE FUNCTION reverse_fqdn(fqdn TEXT) RETURNS TEXT AS $$
BEGIN
    RETURN array_to_string(ARRAY(
        SELECT unnest(string_to_array(fqdn, '.'))
        ORDER BY generate_subscripts(string_to_array(fqdn, '.'), 1) DESC
    ), '.');
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- =============================================================================
-- Function: reverse_pattern
-- Converts suffix pattern to prefix pattern for reversed FQDN matching
-- e.g., "%.google.com" -> "com.google.%"
-- =============================================================================
CREATE OR REPLACE FUNCTION reverse_pattern(pattern TEXT) RETURNS TEXT AS $$
DECLARE
    has_prefix BOOLEAN;
    has_suffix BOOLEAN;
    p TEXT;
    reversed TEXT;
BEGIN
    has_prefix := pattern LIKE '\%%' ESCAPE '\';
    has_suffix := pattern LIKE '%\%' ESCAPE '\';

    -- Remove wildcards and dots
    p := regexp_replace(pattern, '^%\.?', '');
    p := regexp_replace(p, '\.?%$', '');

    -- Reverse the domain parts
    reversed := reverse_fqdn(p);

    -- Re-add wildcards in reversed positions
    IF has_prefix THEN
        reversed := reversed || '.%';
    END IF;
    IF has_suffix THEN
        reversed := '%.' || reversed;
    END IF;

    RETURN reversed;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- =============================================================================
-- Table: lbo_patterns
-- Stores FQDN patterns for Local Breakout routing
-- =============================================================================
CREATE TABLE IF NOT EXISTS lbo_patterns (
    id SERIAL PRIMARY KEY,
    pattern TEXT NOT NULL UNIQUE,            -- Original pattern (e.g., '%.google.com')
    pattern_reversed TEXT NOT NULL,          -- Reversed pattern (e.g., 'com.google.%')
    gateway TEXT NOT NULL,                   -- Target gateway IP for this pattern
    description TEXT,                        -- Human-readable description
    enabled BOOLEAN NOT NULL DEFAULT true,   -- Enable/disable pattern
    priority INTEGER NOT NULL DEFAULT 100,   -- Lower = higher priority
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_lbo_patterns_enabled ON lbo_patterns(enabled);
CREATE INDEX IF NOT EXISTS idx_lbo_patterns_priority ON lbo_patterns(priority);
CREATE INDEX IF NOT EXISTS idx_lbo_patterns_reversed ON lbo_patterns(pattern_reversed text_pattern_ops);

-- =============================================================================
-- Trigger: auto_set_pattern_reversed
-- Automatically sets pattern_reversed when pattern is inserted/updated
-- =============================================================================
CREATE OR REPLACE FUNCTION auto_set_pattern_reversed() RETURNS TRIGGER AS $$
BEGIN
    NEW.pattern_reversed := reverse_pattern(NEW.pattern);
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS lbo_patterns_auto_reverse ON lbo_patterns;

CREATE TRIGGER lbo_patterns_auto_reverse
    BEFORE INSERT OR UPDATE ON lbo_patterns
    FOR EACH ROW
    EXECUTE FUNCTION auto_set_pattern_reversed();

-- =============================================================================
-- Table: lbo_notifications
-- Log of sent notifications (optional, for debugging/auditing)
-- =============================================================================
CREATE TABLE IF NOT EXISTS lbo_notifications (
    id SERIAL PRIMARY KEY,
    fqdn TEXT NOT NULL,
    ip INET NOT NULL,
    pattern_id INTEGER REFERENCES lbo_patterns(id),
    gateway TEXT NOT NULL,
    notified_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_lbo_notifications_time ON lbo_notifications(notified_at);

-- Auto-cleanup old notifications (keep last 7 days)
CREATE OR REPLACE FUNCTION cleanup_old_notifications() RETURNS void AS $$
BEGIN
    DELETE FROM lbo_notifications WHERE notified_at < NOW() - INTERVAL '7 days';
END;
$$ LANGUAGE plpgsql;

-- =============================================================================
-- Function: check_lbo_patterns
-- Called on dns_records INSERT/UPDATE, checks if FQDN matches any LBO pattern
-- Uses reversed FQDN for efficient index-based matching
-- =============================================================================
CREATE OR REPLACE FUNCTION check_lbo_patterns() RETURNS TRIGGER AS $$
DECLARE
    matched_pattern RECORD;
    payload JSON;
BEGIN
    -- Find matching LBO pattern using reversed FQDN (highest priority = lowest number)
    -- This uses the index on fqdn_reversed for efficient prefix matching
    SELECT INTO matched_pattern
        id, pattern, gateway, description
    FROM lbo_patterns
    WHERE enabled = true
      AND NEW.fqdn_reversed LIKE pattern_reversed
    ORDER BY priority ASC, id ASC
    LIMIT 1;

    -- If a pattern matches, send notification
    IF FOUND THEN
        -- Build JSON payload
        payload := json_build_object(
            'event', TG_OP,
            'fqdn', NEW.fqdn,
            'ip', NEW.ip::TEXT,
            'ttl', NEW.ttl,
            'pattern_id', matched_pattern.id,
            'pattern', matched_pattern.pattern,
            'gateway', matched_pattern.gateway,
            'timestamp', NOW()
        );

        -- Send notification on 'lbo_update' channel
        PERFORM pg_notify('lbo_update', payload::TEXT);

        -- Log notification (optional, comment out if not needed)
        INSERT INTO lbo_notifications (fqdn, ip, pattern_id, gateway)
        VALUES (NEW.fqdn, NEW.ip, matched_pattern.id, matched_pattern.gateway);
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- =============================================================================
-- Trigger: dns_records_lbo_trigger
-- Fires on INSERT or UPDATE to dns_records table
-- =============================================================================
DROP TRIGGER IF EXISTS dns_records_lbo_trigger ON dns_records;

CREATE TRIGGER dns_records_lbo_trigger
    AFTER INSERT OR UPDATE ON dns_records
    FOR EACH ROW
    EXECUTE FUNCTION check_lbo_patterns();

-- =============================================================================
-- Function: notify_pattern_change
-- Notifies when LBO patterns are added/modified/deleted
-- =============================================================================
CREATE OR REPLACE FUNCTION notify_pattern_change() RETURNS TRIGGER AS $$
DECLARE
    payload JSON;
BEGIN
    IF TG_OP = 'DELETE' THEN
        payload := json_build_object(
            'event', 'pattern_deleted',
            'pattern_id', OLD.id,
            'pattern', OLD.pattern,
            'timestamp', NOW()
        );
    ELSE
        payload := json_build_object(
            'event', CASE WHEN TG_OP = 'INSERT' THEN 'pattern_added' ELSE 'pattern_updated' END,
            'pattern_id', NEW.id,
            'pattern', NEW.pattern,
            'gateway', NEW.gateway,
            'enabled', NEW.enabled,
            'timestamp', NOW()
        );
    END IF;

    PERFORM pg_notify('lbo_pattern_change', payload::TEXT);

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- =============================================================================
-- Trigger: lbo_patterns_change_trigger
-- Fires on changes to lbo_patterns table
-- =============================================================================
DROP TRIGGER IF EXISTS lbo_patterns_change_trigger ON lbo_patterns;

CREATE TRIGGER lbo_patterns_change_trigger
    AFTER INSERT OR UPDATE OR DELETE ON lbo_patterns
    FOR EACH ROW
    EXECUTE FUNCTION notify_pattern_change();

-- =============================================================================
-- Sample Data: Common SaaS patterns for LBO
-- Note: pattern_reversed is automatically set by trigger
-- =============================================================================
-- INSERT INTO lbo_patterns (pattern, gateway, description, priority) VALUES
-- ('%.zoom.us', '10.0.0.1', 'Zoom video conferencing', 10),
-- ('%.zoom.com', '10.0.0.1', 'Zoom video conferencing', 10),
-- ('%.teams.microsoft.com', '10.0.0.1', 'Microsoft Teams', 20),
-- ('%.office365.com', '10.0.0.1', 'Microsoft 365', 30),
-- ('%.office.com', '10.0.0.1', 'Microsoft Office', 30),
-- ('%.webex.com', '10.0.0.1', 'Cisco Webex', 40),
-- ('%.salesforce.com', '10.0.0.1', 'Salesforce CRM', 50);

-- =============================================================================
-- Utility Views
-- =============================================================================

-- View: Active LBO mappings (patterns + current IPs)
-- Uses reversed FQDN matching for efficiency
CREATE OR REPLACE VIEW lbo_active_mappings AS
SELECT DISTINCT
    p.id AS pattern_id,
    p.pattern,
    p.gateway,
    p.description,
    p.priority,
    r.fqdn,
    r.ip::TEXT AS ip,
    r.ttl,
    r.last_seen
FROM lbo_patterns p
JOIN dns_records r ON r.fqdn_reversed LIKE p.pattern_reversed
WHERE p.enabled = true
  AND r.last_seen > NOW() - INTERVAL '1 hour'
ORDER BY p.priority, r.last_seen DESC;

-- View: Pattern statistics
CREATE OR REPLACE VIEW lbo_pattern_stats AS
SELECT
    p.id,
    p.pattern,
    p.pattern_reversed,
    p.gateway,
    p.enabled,
    COUNT(DISTINCT r.ip) AS unique_ips,
    COUNT(DISTINCT r.fqdn) AS unique_fqdns,
    MAX(r.last_seen) AS last_matched
FROM lbo_patterns p
LEFT JOIN dns_records r ON r.fqdn_reversed LIKE p.pattern_reversed
GROUP BY p.id, p.pattern, p.pattern_reversed, p.gateway, p.enabled
ORDER BY p.priority;
