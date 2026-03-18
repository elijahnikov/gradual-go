package gradual

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
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

	// Fetch initial snapshot
	if err := c.fetchSnapshot(ctx); err != nil {
		c.initErr = fmt.Errorf("gradual: snapshot fetch failed: %w", err)
		return
	}

	// Start polling
	if c.pollingEnabled {
		go c.poll(ctx)
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
				c.listenersMu.Lock()
				for _, cb := range c.updateListeners {
					cb()
				}
				c.listenersMu.Unlock()
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

// Close stops polling and flushes pending events.
func (c *Client) Close() {
	close(c.stopPolling)
	if c.eventBuffer != nil {
		c.eventBuffer.Destroy()
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
