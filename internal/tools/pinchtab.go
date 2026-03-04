package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	pinchtabDefaultAddr = "127.0.0.1:9867"
	pinchtabStartupWait = 10 * time.Second
)

// pinchtabClient is an HTTP client for the pinchtab browser automation server.
type pinchtabClient struct {
	mu      sync.Mutex
	base    string
	http    *http.Client
	cmd     *exec.Cmd // nil if user started pinchtab externally
	started bool
}

func newPinchtabClient() *pinchtabClient {
	return &pinchtabClient{
		base: "http://" + pinchtabDefaultAddr,
		http: &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: 4},
		},
	}
}

// ensure checks if pinchtab is running, and starts it if the binary is available.
func (c *pinchtabClient) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.healthy(ctx) {
		return nil
	}

	// Try to start pinchtab binary
	bin, err := exec.LookPath("pinchtab")
	if err != nil {
		return fmt.Errorf("pinchtab not found in PATH: %w", err)
	}

	c.cmd = exec.Command(bin)
	c.cmd.Stdout = os.Stderr // log pinchtab output to stderr
	c.cmd.Stderr = os.Stderr
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pinchtab: %w", err)
	}

	// Wait for healthy, respecting caller cancellation
	deadline := time.After(pinchtabStartupWait)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.cmd.Process.Kill()
			c.cmd.Wait()
			c.cmd = nil
			return fmt.Errorf("pinchtab startup cancelled: %w", ctx.Err())
		case <-deadline:
			c.cmd.Process.Kill()
			c.cmd.Wait()
			c.cmd = nil
			return fmt.Errorf("pinchtab failed to start within %s", pinchtabStartupWait)
		case <-tick.C:
			// Use a short-lived context for health check so a cancelled parent doesn't
			// prevent us from detecting a healthy server on the deadline path.
			hctx, hcancel := context.WithTimeout(context.Background(), 2*time.Second)
			ok := c.healthy(hctx)
			hcancel()
			if ok {
				c.started = true
				return nil
			}
		}
	}
}

func (c *pinchtabClient) healthy(ctx context.Context) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *pinchtabClient) available(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.healthy(ctx)
}

// close shuts down the pinchtab process if we started it.
func (c *pinchtabClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		// Graceful shutdown via API
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "POST", c.base+"/shutdown", nil)
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		// Wait briefly, then force kill
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			c.cmd.Process.Kill()
		}
		c.cmd = nil
		c.started = false
	}
}

// --- API methods ---

type ptNavigateReq struct {
	URL         string `json:"url"`
	TabID       string `json:"tabId,omitempty"`
	NewTab      bool   `json:"newTab,omitempty"`
	BlockImages bool   `json:"blockImages,omitempty"`
}

type ptNavigateResp struct {
	TabID string `json:"tabId"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (c *pinchtabClient) navigate(ctx context.Context, req ptNavigateReq) (*ptNavigateResp, error) {
	var resp ptNavigateResp
	if err := c.postJSON(ctx, "/navigate", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type ptActionReq struct {
	TabID    string `json:"tabId,omitempty"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	ScrollY  int    `json:"scrollY,omitempty"`
	WaitNav  bool   `json:"waitNav,omitempty"`
}

type ptActionResp struct {
	Success bool           `json:"success"`
	Result  map[string]any `json:"result"`
}

func (c *pinchtabClient) action(ctx context.Context, req ptActionReq) (*ptActionResp, error) {
	var resp ptActionResp
	if err := c.postJSON(ctx, "/action", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type ptSnapshotNode struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Depth    int    `json:"depth"`
	Value    string `json:"value,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Focused  bool   `json:"focused,omitempty"`
}

type ptSnapshotResp struct {
	URL   string           `json:"url"`
	Title string           `json:"title"`
	Nodes []ptSnapshotNode `json:"nodes"`
	Count int              `json:"count"`
}

func (c *pinchtabClient) snapshot(ctx context.Context, tabID, filter string) (*ptSnapshotResp, error) {
	q := url.Values{}
	if tabID != "" {
		q.Set("tabId", tabID)
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	var resp ptSnapshotResp
	if err := c.getJSON(ctx, "/snapshot?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type ptFindReq struct {
	Query string `json:"query"`
	TabID string `json:"tabId,omitempty"`
	TopK  int    `json:"topK,omitempty"`
}

type ptFindMatch struct {
	Ref   string  `json:"ref"`
	Score float64 `json:"score"`
	Role  string  `json:"role"`
	Name  string  `json:"name"`
}

type ptFindResp struct {
	BestRef    string        `json:"best_ref"`
	Confidence string        `json:"confidence"`
	Score      float64       `json:"score"`
	Matches    []ptFindMatch `json:"matches"`
}

func (c *pinchtabClient) find(ctx context.Context, req ptFindReq) (*ptFindResp, error) {
	var resp ptFindResp
	if err := c.postJSON(ctx, "/find", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type ptTextResp struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (c *pinchtabClient) text(ctx context.Context, tabID string) (*ptTextResp, error) {
	q := url.Values{}
	if tabID != "" {
		q.Set("tabId", tabID)
	}
	var resp ptTextResp
	if err := c.getJSON(ctx, "/text?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pinchtabClient) screenshot(ctx context.Context, tabID string) ([]byte, error) {
	q := url.Values{"raw": {"true"}}
	if tabID != "" {
		q.Set("tabId", tabID)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/screenshot?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("screenshot: %s: %s", resp.Status, body)
	}
	return io.ReadAll(resp.Body)
}

type ptEvalResp struct {
	Result any `json:"result"`
}

func (c *pinchtabClient) evaluate(ctx context.Context, tabID, expr string) (*ptEvalResp, error) {
	body := map[string]string{"expression": expr}
	if tabID != "" {
		body["tabId"] = tabID
	}
	var resp ptEvalResp
	if err := c.postJSON(ctx, "/evaluate", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type ptTabsResp struct {
	Tabs []struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"tabs"`
}

func (c *pinchtabClient) tabs(ctx context.Context) (*ptTabsResp, error) {
	var resp ptTabsResp
	if err := c.getJSON(ctx, "/tabs", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- HTTP helpers ---

func (c *pinchtabClient) postJSON(ctx context.Context, path string, reqBody any, respBody any) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", resp.Status, body)
	}
	if respBody != nil {
		return json.Unmarshal(body, respBody)
	}
	return nil
}

func (c *pinchtabClient) getJSON(ctx context.Context, path string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", resp.Status, body)
	}
	if respBody != nil {
		return json.Unmarshal(body, respBody)
	}
	return nil
}
