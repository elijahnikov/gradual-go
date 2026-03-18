package gradual

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const sdkVersion = "0.1.0"

// EventMeta contains metadata for event payloads.
type EventMeta struct {
	ProjectID      string `json:"projectId"`
	OrganizationID string `json:"organizationId"`
	EnvironmentID  string `json:"environmentId"`
	SDKPlatform    string `json:"sdkPlatform,omitempty"`
}

// EventBufferOptions configures the event buffer.
type EventBufferOptions struct {
	BaseURL         string
	APIKey          string
	Meta            EventMeta
	FlushIntervalMs int
	MaxBatchSize    int
}

// EventBuffer batches and delivers evaluation events.
type EventBuffer struct {
	opts    EventBufferOptions
	events  []map[string]interface{}
	mu      sync.Mutex
	ticker  *time.Ticker
	stop    chan struct{}
	client  *http.Client
}

// NewEventBuffer creates a new event buffer that flushes periodically.
func NewEventBuffer(opts EventBufferOptions) *EventBuffer {
	if opts.FlushIntervalMs == 0 {
		opts.FlushIntervalMs = 30000
	}
	if opts.MaxBatchSize == 0 {
		opts.MaxBatchSize = 100
	}

	eb := &EventBuffer{
		opts:   opts,
		stop:   make(chan struct{}),
		client: &http.Client{Timeout: 10 * time.Second},
	}

	eb.ticker = time.NewTicker(time.Duration(opts.FlushIntervalMs) * time.Millisecond)
	go eb.flushLoop()

	return eb
}

func (eb *EventBuffer) flushLoop() {
	for {
		select {
		case <-eb.ticker.C:
			eb.Flush()
		case <-eb.stop:
			return
		}
	}
}

// Push adds an event to the buffer.
func (eb *EventBuffer) Push(event map[string]interface{}) {
	var batch []map[string]interface{}
	eb.mu.Lock()
	eb.events = append(eb.events, event)
	if len(eb.events) >= eb.opts.MaxBatchSize {
		batch = eb.events[:eb.opts.MaxBatchSize]
		eb.events = eb.events[eb.opts.MaxBatchSize:]
	}
	eb.mu.Unlock()

	if batch != nil {
		eb.send(batch)
	}
}

// Flush sends all pending events.
func (eb *EventBuffer) Flush() {
	eb.mu.Lock()
	if len(eb.events) == 0 {
		eb.mu.Unlock()
		return
	}
	n := eb.opts.MaxBatchSize
	if n > len(eb.events) {
		n = len(eb.events)
	}
	batch := make([]map[string]interface{}, n)
	copy(batch, eb.events[:n])
	eb.events = eb.events[n:]
	eb.mu.Unlock()

	eb.send(batch)
}

func (eb *EventBuffer) send(batch []map[string]interface{}) {
	payload := map[string]interface{}{
		"meta": map[string]interface{}{
			"projectId":      eb.opts.Meta.ProjectID,
			"organizationId": eb.opts.Meta.OrganizationID,
			"environmentId":  eb.opts.Meta.EnvironmentID,
			"sdkVersion":     sdkVersion,
			"sdkPlatform":    eb.opts.Meta.SDKPlatform,
		},
		"events": batch,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", eb.opts.BaseURL+"/sdk/evaluations", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+eb.opts.APIKey)

	resp, err := eb.client.Do(req)
	if err != nil {
		return // Fire-and-forget
	}
	resp.Body.Close()
}

// Destroy stops the flush timer and flushes remaining events.
func (eb *EventBuffer) Destroy() {
	eb.ticker.Stop()
	close(eb.stop)
	eb.Flush()
}
