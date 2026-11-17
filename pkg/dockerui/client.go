package dockerui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Bounds of every Docker response read into memory.
const (
	listLimit    = 8 << 20
	inspectLimit = 4 << 20
	infoLimit    = 1 << 20
	statsLimit   = 1 << 20
	errorLimit   = 64 << 10
)

// ErrUnavailable means the Docker socket cannot be reached.
var ErrUnavailable = errors.New("docker injoignable")

// errTimeout means Docker did not answer before the call's deadline.
var errTimeout = errors.New("Docker n’a pas répondu à temps")

// unavailableError keeps the dial error for the message shown to the admin.
type unavailableError struct{ cause error }

func (e *unavailableError) Error() string { return "Docker injoignable : " + e.cause.Error() }
func (e *unavailableError) Unwrap() error { return ErrUnavailable }

// APIError is an error status returned by the Engine API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Docker a répondu %d", e.Status)
	}
	return e.Message
}

// apiClient speaks HTTP to the Engine API over its unix socket. Every call
// carries a context deadline; there is no client-wide timeout because a stop
// legitimately waits for the container's grace period.
type apiClient struct {
	http   *http.Client
	socket string
}

func newAPIClient(socket string) *apiClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression:  true,
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
	}
	return &apiClient{http: &http.Client{Transport: transport}, socket: socket}
}

// do sends one request. Paths are unversioned so the daemon answers with its
// own API version: pinning one would break on engines that dropped it.
func (c *apiClient) do(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	return c.doBody(ctx, method, path, query, nil)
}

// doBody is do with an optional JSON body.
func (c *apiClient) doBody(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Response, error) {
	target := "http://docker" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "tmuxatlas-dockerui")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errTimeout
		}
		return nil, &unavailableError{cause: unwrapURLError(err)}
	}
	return resp, nil
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// apiErrorFrom reads Docker's {"message": "..."} error body, bounded.
func apiErrorFrom(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorLimit))
	var payload struct {
		Message string `json:"message"`
	}
	message := ""
	if json.Unmarshal(body, &payload) == nil {
		message = payload.Message
	} else {
		message = strings.TrimSpace(string(body))
	}
	return &APIError{Status: resp.StatusCode, Message: message}
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("réponse Docker trop volumineuse (plus de %d octets)", limit)
	}
	return body, nil
}

// getRaw returns the body of a successful GET, bounded by limit.
func (c *apiClient) getRaw(ctx context.Context, path string, query url.Values, limit int64) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, path, query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErrorFrom(resp)
	}
	return readLimited(resp.Body, limit)
}

func (c *apiClient) getJSON(ctx context.Context, path string, query url.Values, limit int64, out any) error {
	body, err := c.getRaw(ctx, path, query, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("réponse Docker illisible : %w", err)
	}
	return nil
}

func (c *apiClient) listContainers(ctx context.Context) ([]apiContainer, error) {
	var out []apiContainer
	err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, listLimit, &out)
	return out, err
}

func (c *apiClient) info(ctx context.Context) (apiInfo, error) {
	var out apiInfo
	err := c.getJSON(ctx, "/info", nil, infoLimit, &out)
	return out, err
}

// inspect returns the raw inspect document of a full container ID.
func (c *apiClient) inspect(ctx context.Context, id string) ([]byte, error) {
	return c.getRaw(ctx, "/containers/"+id+"/json", nil, inspectLimit)
}

// stats reads one sample. oneShot skips the daemon's second sample (one to
// two seconds); the caller then computes CPU from its own previous sample.
// Engines before API 1.41 ignore it and send both samples.
func (c *apiClient) stats(ctx context.Context, id string, oneShot bool) (apiStats, error) {
	query := url.Values{"stream": {"false"}}
	if oneShot {
		query.Set("one-shot", "true")
	}
	var out apiStats
	err := c.getJSON(ctx, "/containers/"+id+"/stats", query, statsLimit, &out)
	return out, err
}

// post sends a state change. It reports false when Docker answers 304: the
// container was already in the requested state.
func (c *apiClient) post(ctx context.Context, path string) (bool, error) {
	return c.send(ctx, http.MethodPost, path)
}

// remove deletes a stopped container. It never passes force nor v: Docker
// refuses a running container and keeps its anonymous volumes.
func (c *apiClient) remove(ctx context.Context, id string) error {
	_, err := c.send(ctx, http.MethodDelete, "/containers/"+id)
	return err
}

func (c *apiClient) send(ctx context.Context, method, path string) (bool, error) {
	resp, err := c.do(ctx, method, path, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return false, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorLimit))
		return true, nil
	default:
		return false, apiErrorFrom(resp)
	}
}

// logs opens the log stream. The caller closes the body and bounds its reads.
func (c *apiClient) logs(ctx context.Context, id string, query url.Values) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", query)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, apiErrorFrom(resp)
	}
	return resp, nil
}
