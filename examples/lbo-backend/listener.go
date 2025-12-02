// Package main provides a sample LBO Backend that listens to PostgreSQL
// notifications and can forward them to LBO Agents via gRPC.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// LBOUpdate represents a notification from PostgreSQL when a DNS record
// matches an LBO pattern
type LBOUpdate struct {
	Event     string    `json:"event"`      // "INSERT" or "UPDATE"
	FQDN      string    `json:"fqdn"`       // Resolved domain name
	IP        string    `json:"ip"`         // Resolved IP address
	TTL       int       `json:"ttl"`        // DNS TTL
	PatternID int       `json:"pattern_id"` // Matched pattern ID
	Pattern   string    `json:"pattern"`    // Matched pattern string
	Gateway   string    `json:"gateway"`    // Target gateway for routing
	Timestamp time.Time `json:"timestamp"`  // When the update occurred
}

// PatternChange represents a notification when LBO patterns are modified
type PatternChange struct {
	Event     string    `json:"event"`      // "pattern_added", "pattern_updated", "pattern_deleted"
	PatternID int       `json:"pattern_id"` // Pattern ID
	Pattern   string    `json:"pattern"`    // Pattern string
	Gateway   string    `json:"gateway"`    // Target gateway (empty for delete)
	Enabled   bool      `json:"enabled"`    // Is pattern enabled
	Timestamp time.Time `json:"timestamp"`  // When the change occurred
}

// LBOListener listens to PostgreSQL notifications
type LBOListener struct {
	dsn       string
	listener  *pq.Listener
	handlers  []UpdateHandler
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// UpdateHandler is a callback function for LBO updates
type UpdateHandler func(update *LBOUpdate)

// PatternChangeHandler is a callback function for pattern changes
type PatternChangeHandler func(change *PatternChange)

// NewLBOListener creates a new PostgreSQL listener
func NewLBOListener(dsn string) *LBOListener {
	ctx, cancel := context.WithCancel(context.Background())
	return &LBOListener{
		dsn:    dsn,
		ctx:    ctx,
		cancel: cancel,
	}
}

// OnUpdate registers a handler for LBO updates
func (l *LBOListener) OnUpdate(handler UpdateHandler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handlers = append(l.handlers, handler)
}

// Start begins listening for PostgreSQL notifications
func (l *LBOListener) Start() error {
	// Create listener with error callback
	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("Listener error: %v", err)
		}
		switch ev {
		case pq.ListenerEventConnected:
			log.Println("Connected to PostgreSQL")
		case pq.ListenerEventDisconnected:
			log.Println("Disconnected from PostgreSQL")
		case pq.ListenerEventReconnected:
			log.Println("Reconnected to PostgreSQL")
		case pq.ListenerEventConnectionAttemptFailed:
			log.Printf("Connection attempt failed: %v", err)
		}
	}

	l.listener = pq.NewListener(l.dsn, 10*time.Second, time.Minute, reportProblem)

	// Subscribe to channels
	if err := l.listener.Listen("lbo_update"); err != nil {
		return fmt.Errorf("failed to listen on lbo_update: %w", err)
	}
	log.Println("Subscribed to channel: lbo_update")

	if err := l.listener.Listen("lbo_pattern_change"); err != nil {
		return fmt.Errorf("failed to listen on lbo_pattern_change: %w", err)
	}
	log.Println("Subscribed to channel: lbo_pattern_change")

	// Start notification handler
	go l.handleNotifications()

	return nil
}

// handleNotifications processes incoming PostgreSQL notifications
func (l *LBOListener) handleNotifications() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case notification := <-l.listener.Notify:
			if notification == nil {
				// Connection lost, will reconnect automatically
				continue
			}

			switch notification.Channel {
			case "lbo_update":
				l.handleLBOUpdate(notification.Extra)
			case "lbo_pattern_change":
				l.handlePatternChange(notification.Extra)
			}
		case <-time.After(90 * time.Second):
			// Ping to keep connection alive
			go func() {
				if err := l.listener.Ping(); err != nil {
					log.Printf("Ping failed: %v", err)
				}
			}()
		}
	}
}

// handleLBOUpdate processes LBO update notifications
func (l *LBOListener) handleLBOUpdate(payload string) {
	var update LBOUpdate
	if err := json.Unmarshal([]byte(payload), &update); err != nil {
		log.Printf("Failed to parse LBO update: %v", err)
		return
	}

	log.Printf("LBO Update: %s -> %s (pattern: %s, gateway: %s)",
		update.FQDN, update.IP, update.Pattern, update.Gateway)

	// Call registered handlers
	l.mu.RLock()
	handlers := l.handlers
	l.mu.RUnlock()

	for _, handler := range handlers {
		handler(&update)
	}
}

// handlePatternChange processes pattern change notifications
func (l *LBOListener) handlePatternChange(payload string) {
	var change PatternChange
	if err := json.Unmarshal([]byte(payload), &change); err != nil {
		log.Printf("Failed to parse pattern change: %v", err)
		return
	}

	log.Printf("Pattern Change: %s - pattern=%s, gateway=%s, enabled=%v",
		change.Event, change.Pattern, change.Gateway, change.Enabled)
}

// Stop stops the listener
func (l *LBOListener) Stop() error {
	l.cancel()
	if l.listener != nil {
		return l.listener.Close()
	}
	return nil
}

// =============================================================================
// Sample gRPC Integration (pseudo-code structure)
// =============================================================================

// AgentManager manages connected LBO agents
type AgentManager struct {
	agents map[string]*Agent
	mu     sync.RWMutex
}

// Agent represents a connected LBO agent
type Agent struct {
	ID       string
	Patterns []string // Patterns this agent is interested in
	// In real implementation: gRPC stream connection
}

// NewAgentManager creates a new agent manager
func NewAgentManager() *AgentManager {
	return &AgentManager{
		agents: make(map[string]*Agent),
	}
}

// RegisterAgent registers a new agent
func (m *AgentManager) RegisterAgent(id string, patterns []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[id] = &Agent{ID: id, Patterns: patterns}
	log.Printf("Agent registered: %s (patterns: %v)", id, patterns)
}

// UnregisterAgent removes an agent
func (m *AgentManager) UnregisterAgent(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, id)
	log.Printf("Agent unregistered: %s", id)
}

// BroadcastUpdate sends an update to all relevant agents
func (m *AgentManager) BroadcastUpdate(update *LBOUpdate) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, agent := range m.agents {
		// Check if agent is interested in this pattern
		for _, p := range agent.Patterns {
			if p == update.Pattern || p == "*" {
				// In real implementation: send via gRPC stream
				log.Printf("  -> Sending to agent %s: %s -> %s via %s",
					agent.ID, update.FQDN, update.IP, update.Gateway)
				break
			}
		}
	}
}

// =============================================================================
// Main
// =============================================================================

func main() {
	dsn := flag.String("dsn", "host=localhost port=5432 user=coredns dbname=coredns sslmode=disable",
		"PostgreSQL connection string")
	flag.Parse()

	log.Println("Starting LBO Backend Listener...")

	// Create listener
	listener := NewLBOListener(*dsn)

	// Create agent manager (for gRPC integration)
	agentManager := NewAgentManager()

	// Register sample agents (in real app, agents would connect via gRPC)
	agentManager.RegisterAgent("agent-1", []string{"%.zoom.us", "%.teams.microsoft.com"})
	agentManager.RegisterAgent("agent-2", []string{"*"}) // All patterns

	// Register update handler
	listener.OnUpdate(func(update *LBOUpdate) {
		// Forward to agents via gRPC
		agentManager.BroadcastUpdate(update)

		// You could also:
		// - Update local routing table
		// - Write to a message queue
		// - Call a webhook
		// - Update a cache
	})

	// Start listening
	if err := listener.Start(); err != nil {
		log.Fatalf("Failed to start listener: %v", err)
	}

	log.Println("Listening for LBO updates... Press Ctrl+C to stop.")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	if err := listener.Stop(); err != nil {
		log.Printf("Error stopping listener: %v", err)
	}
}
