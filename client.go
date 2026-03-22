package gradual

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const defaultBaseURL = "https://worker.gradual.so/api/v1"

// Options configures the Gradual client.
type Options struct {
	APIKey            string
	Environment       string
	BaseURL           string
	PollingEnabled    bool
	PollingIntervalMs int
	EventsEnabled     bool
	EventsFlushMs     int
	EventsMaxBatch    int
	RealtimeEnabled   bool
}

// Client is a feature flag client that connects to the Gradual edge service.
type Client struct {
	apiKey      string
	environment string
	baseURL     string
	httpClient  *http.Client
	snapshot    *EnvironmentSnapshot
	mu          sync.RWMutex
	etag        string
	ready       chan struct{}
	initErr     error

	identifiedContext EvaluationContext
	eventBuffer       *EventBuffer

	pollingEnabled bool
	pollingInterval time.Duration
	stopPolling     chan struct{}

	eventsEnabled  bool
	eventsFlushMs  int
	eventsMaxBatch int

	realtimeEnabled bool
	wsConn          *websocket.Conn
	wsMu            sync.Mutex
	stopWS          chan struct{}
	wsCancel        context.CancelFunc

	updateListeners []func()
	listenersMu     sync.Mutex
}

// NewClient creates a new Gradual client that begins initialization immediately.
func NewClient(ctx context.Context, opts Options) *Client {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	pollingInterval := time.Duration(opts.PollingIntervalMs) * time.Millisecond
	if pollingInterval == 0 {
		pollingInterval = 10 * time.Second
	}

	realtimeEnabled := opts.RealtimeEnabled
	// Default to true if not explicitly set. Since Go zero-value for bool is
	// false, we treat the zero-value Options (where PollingEnabled is also
	// false) as "realtime enabled by default". Users who want to disable
	// realtime should set RealtimeEnabled = false explicitly. We detect
	// the "unset" case by checking if neither polling nor realtime were
	// explicitly enabled — in that case we default realtime on.
	// However, the simplest convention matching the TS SDK: default true.
	// We use a helper: if the user hasn't set any transport preference, enable realtime.
	if !opts.PollingEnabled && !opts.RealtimeEnabled {
		realtimeEnabled = true
	}

	c := &Client{
		apiKey:          opts.APIKey,
		environment:     opts.Environment,
		baseURL:         baseURL,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		ready:           make(chan struct{}),
		pollingEnabled:  opts.PollingEnabled,
		pollingInterval: pollingInterval,
		eventsEnabled:   opts.EventsEnabled,
		eventsFlushMs:   opts.EventsFlushMs,
		eventsMaxBatch:  opts.EventsMaxBatch,
		stopPolling:     make(chan struct{}),
		realtimeEnabled: realtimeEnabled,
		stopWS:          make(chan struct{}),
	}

	go c.init(ctx)
	return c
}

func (c *Client) init(ctx context.Context) {
	defer close(c.ready)

	// Validate API key
	body, _ := json.Marshal(map[string]string{"apiKey": c.apiKey})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/sdk/init", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.initErr = fmt.Errorf("gradual: init failed: %w", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.initErr = fmt.Errorf("gradual: init returned %d", resp.StatusCode)
		return
	}

	var initData struct {
		Valid          bool   `json:"valid"`
		Error          string `json:"error"`
		MauLimitReached bool  `json:"mauLimitReached"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initData); err != nil {
		c.initErr = fmt.Errorf("gradual: init decode failed: %w", err)
		return
	}
	if !initData.Valid {
		c.initErr = fmt.Errorf("gradual: invalid API key - %s", initData.Error)
		return
	}

	// Try WebSocket for initial snapshot + real-time updates
	wsConnected := false
	if c.realtimeEnabled {
		if err := c.connectWebSocket(ctx); err == nil {
			wsConnected = true
		}
		// If WebSocket fails, fall through to HTTP fetch + polling
	}

	if !wsConnected {
		// Fetch initial snapshot via HTTP
		if err := c.fetchSnapshot(ctx); err != nil {
			c.initErr = fmt.Errorf("gradual: snapshot fetch failed: %w", err)
			return
		}

		// Start polling
		if c.pollingEnabled {
			go c.poll(ctx)
		}
	}

	// Initialize event buffer
	if c.eventsEnabled {
		c.mu.RLock()
		snap := c.snapshot
		c.mu.RUnlock()
		if snap != nil {
			flushMs := c.eventsFlushMs
			if flushMs == 0 {
				flushMs = 30000
			}
			maxBatch := c.eventsMaxBatch
			if maxBatch == 0 {
				maxBatch = 100
			}
			c.eventBuffer = NewEventBuffer(EventBufferOptions{
				BaseURL:    c.baseURL,
				APIKey:     c.apiKey,
				Meta: EventMeta{
					ProjectID:      snap.Meta.ProjectID,
					OrganizationID: snap.Meta.OrganizationID,
					EnvironmentID:  snap.Meta.EnvironmentID,
					SDKPlatform:    "go",
				},
				FlushIntervalMs: flushMs,
				MaxBatchSize:    maxBatch,
			})
		}
	}
}

func (c *Client) fetchSnapshot(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/sdk/snapshot?environment=%s", c.baseURL, c.environment), nil)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	c.mu.RLock()
	etag := c.etag
	c.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snapshot returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var snap EnvironmentSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}

	c.mu.Lock()
	c.snapshot = &snap
	if e := resp.Header.Get("ETag"); e != "" {
		c.etag = e
	}
	c.mu.Unlock()

	return nil
}

func (c *Client) poll(ctx context.Context) {
	ticker := time.NewTicker(c.pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopPolling:
			return
		case <-ticker.C:
			c.mu.RLock()
			prevVersion := 0
			if c.snapshot != nil {
				prevVersion = c.snapshot.Version
			}
			c.mu.RUnlock()

			if err := c.fetchSnapshot(ctx); err != nil {
				continue
			}

			c.mu.RLock()
			newVersion := 0
			if c.snapshot != nil {
				newVersion = c.snapshot.Version
			}
			c.mu.RUnlock()

			if newVersion != prevVersion {
				c.notifyListeners()
			}
		}
	}
}

// WaitUntilReady blocks until the client is initialized.
func (c *Client) WaitUntilReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return c.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsReady returns true if the client has been initialized.
func (c *Client) IsReady() bool {
	select {
	case <-c.ready:
		return c.initErr == nil
	default:
		return false
	}
}

// IsEnabled checks if a boolean flag is enabled.
func (c *Client) IsEnabled(key string, ctx ...EvaluationContext) bool {
	var context EvaluationContext
	if len(ctx) > 0 {
		context = c.mergeContext(ctx[0])
	} else {
		context = c.mergeContext(nil)
	}

	c.mu.RLock()
	snap := c.snapshot
	c.mu.RUnlock()
	if snap == nil {
		return false
	}

	flag, ok := snap.Flags[key]
	if !ok {
		return false
	}

	result := EvaluateFlag(flag, context, snap.Segments)
	if v, ok := result.Value.(bool); ok {
		return v
	}
	return false
}

// Get returns a flag value, falling back to the provided default.
func (c *Client) Get(key string, fallback interface{}, ctx ...EvaluationContext) interface{} {
	var context EvaluationContext
	if len(ctx) > 0 {
		context = c.mergeContext(ctx[0])
	} else {
		context = c.mergeContext(nil)
	}

	c.mu.RLock()
	snap := c.snapshot
	c.mu.RUnlock()
	if snap == nil {
		return fallback
	}

	flag, ok := snap.Flags[key]
	if !ok {
		return fallback
	}

	result := EvaluateFlag(flag, context, snap.Segments)
	if result.Value != nil {
		return result.Value
	}
	return fallback
}

// Identify sets persistent user context for all evaluations.
func (c *Client) Identify(context EvaluationContext) {
	c.mu.Lock()
	c.identifiedContext = context
	c.mu.Unlock()
}

// Reset clears the identified user context.
func (c *Client) Reset() {
	c.mu.Lock()
	c.identifiedContext = nil
	c.mu.Unlock()
}

// OnUpdate subscribes to snapshot updates. Returns an unsubscribe function.
func (c *Client) OnUpdate(callback func()) func() {
	c.listenersMu.Lock()
	c.updateListeners = append(c.updateListeners, callback)
	idx := len(c.updateListeners) - 1
	c.listenersMu.Unlock()

	return func() {
		c.listenersMu.Lock()
		if idx < len(c.updateListeners) {
			c.updateListeners = append(c.updateListeners[:idx], c.updateListeners[idx+1:]...)
		}
		c.listenersMu.Unlock()
	}
}

// Close stops polling, closes WebSocket, and flushes pending events.
func (c *Client) Close() {
	close(c.stopPolling)

	// Close WebSocket
	select {
	case <-c.stopWS:
		// already closed
	default:
		close(c.stopWS)
	}
	if c.wsCancel != nil {
		c.wsCancel()
	}
	c.wsMu.Lock()
	if c.wsConn != nil {
		c.wsConn.CloseNow()
		c.wsConn = nil
	}
	c.wsMu.Unlock()

	if c.eventBuffer != nil {
		c.eventBuffer.Destroy()
	}
}

// wsURL builds the WebSocket URL from the base URL.
func (c *Client) wsURL() string {
	u := c.baseURL
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)

	params := url.Values{}
	params.Set("apiKey", c.apiKey)
	params.Set("environment", c.environment)

	return u + "/sdk/connect?" + params.Encode()
}

// connectWebSocket dials the WebSocket and reads the first message as the initial snapshot.
// On success it starts a goroutine to listen for subsequent updates.
func (c *Client) connectWebSocket(ctx context.Context) error {
	wsCtx, cancel := context.WithCancel(context.Background())
	c.wsCancel = cancel

	conn, _, err := websocket.Dial(ctx, c.wsURL(), nil)
	if err != nil {
		cancel()
		return fmt.Errorf("gradual: websocket dial failed: %w", err)
	}

	// Read the first message — initial snapshot
	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		cancel()
		return fmt.Errorf("gradual: websocket initial read failed: %w", err)
	}

	var snap EnvironmentSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		conn.CloseNow()
		cancel()
		return fmt.Errorf("gradual: websocket snapshot parse failed: %w", err)
	}

	c.mu.Lock()
	c.snapshot = &snap
	c.mu.Unlock()

	c.wsMu.Lock()
	c.wsConn = conn
	c.wsMu.Unlock()

	// Start listening for updates
	go c.wsListen(wsCtx, conn)

	return nil
}

// wsListen reads messages from the WebSocket and updates the snapshot.
// On disconnect it schedules a reconnect with exponential backoff.
func (c *Client) wsListen(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Check if we've been told to stop
			select {
			case <-c.stopWS:
				return
			default:
			}

			// Connection error — close and reconnect
			conn.CloseNow()
			c.wsMu.Lock()
			c.wsConn = nil
			c.wsMu.Unlock()

			go c.wsReconnect(ctx)
			return
		}

		var snap EnvironmentSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}

		c.mu.RLock()
		prevVersion := 0
		if c.snapshot != nil {
			prevVersion = c.snapshot.Version
		}
		c.mu.RUnlock()

		c.mu.Lock()
		c.snapshot = &snap
		c.mu.Unlock()

		if snap.Version != prevVersion {
			c.notifyListeners()
		}
	}
}

// wsReconnect attempts to reconnect the WebSocket with exponential backoff.
func (c *Client) wsReconnect(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-c.stopWS:
			return
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		conn, _, err := websocket.Dial(ctx, c.wsURL(), nil)
		if err != nil {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Read first message as snapshot
		_, data, err := conn.Read(ctx)
		if err != nil {
			conn.CloseNow()
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		var snap EnvironmentSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			conn.CloseNow()
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		c.mu.RLock()
		prevVersion := 0
		if c.snapshot != nil {
			prevVersion = c.snapshot.Version
		}
		c.mu.RUnlock()

		c.mu.Lock()
		c.snapshot = &snap
		c.mu.Unlock()

		c.wsMu.Lock()
		c.wsConn = conn
		c.wsMu.Unlock()

		if snap.Version != prevVersion {
			c.notifyListeners()
		}

		// Resume listening
		go c.wsListen(ctx, conn)
		return
	}
}

// notifyListeners calls all registered update callbacks.
func (c *Client) notifyListeners() {
	c.listenersMu.Lock()
	listeners := make([]func(), len(c.updateListeners))
	copy(listeners, c.updateListeners)
	c.listenersMu.Unlock()

	for _, cb := range listeners {
		cb()
	}
}

func (c *Client) mergeContext(context EvaluationContext) EvaluationContext {
	c.mu.RLock()
	identified := c.identifiedContext
	c.mu.RUnlock()

	merged := EvaluationContext{}
	allKinds := map[string]bool{}

	for k := range identified {
		allKinds[k] = true
	}
	for k := range context {
		allKinds[k] = true
	}

	for kind := range allKinds {
		merged[kind] = map[string]interface{}{}
		if id, ok := identified[kind]; ok {
			for k, v := range id {
				merged[kind][k] = v
			}
		}
		if ctx, ok := context[kind]; ok {
			for k, v := range ctx {
				merged[kind][k] = v
			}
		}
	}

	return merged
}
