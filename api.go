package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBodyBytes = 16 << 20 // 16 MiB

var allowedSources = []string{"TCC", "ICC", "HNAP", "HNAC", "LNAC"}

var version = "dev"

type AnalogOutput struct {
	High   float64 `json:"high"`
	Log    bool    `json:"log"`
	Low    float64 `json:"low"`
	Source string  `json:"source"`
}

type ConnectionConfig struct {
	BaseURL         *url.URL
	Username        string
	Password        string
	Timeout         time.Duration
	AllowInvalidTLS bool
}

type settingsDocument map[string]json.RawMessage

type HTTPError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	message := fmt.Sprintf("%s %s returned %s", e.Method, e.URL, e.Status)
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

type APIClient struct {
	baseURL   *url.URL
	username  string
	password  string
	http      *http.Client
	transport *http.Transport
}

func normalizeBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("device address is required")
	}
	if strings.ContainsAny(raw, "\r\n\t") {
		return nil, errors.New("device address contains invalid whitespace")
	}

	if !strings.Contains(raw, "://") {
		// A raw IPv6 address needs brackets before it can be used as a URL host.
		if ip := net.ParseIP(raw); ip != nil && strings.Contains(raw, ":") {
			raw = "[" + raw + "]"
		}
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid device address: %w", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("only http and https addresses are supported")
	}
	if u.Host == "" {
		return nil, errors.New("device address has no host")
	}
	if u.User != nil {
		return nil, errors.New("do not put credentials in the device address")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("device address must not contain a query or fragment")
	}

	path := strings.TrimRight(strings.TrimSpace(u.Path), "/")
	if path == "" {
		path = "/api"
	} else if !strings.EqualFold(path, "/api") && !strings.HasSuffix(strings.ToLower(path), "/api") {
		path += "/api"
	}
	u.Path = path + "/"
	u.RawPath = ""
	return u, nil
}

func newAPIClient(cfg ConnectionConfig) (*APIClient, error) {
	if cfg.BaseURL == nil {
		return nil, errors.New("missing API base URL")
	}
	if cfg.Username == "" {
		return nil, errors.New("username is required")
	}
	if strings.Contains(cfg.Username, ":") {
		return nil, errors.New("username must not contain ':' when using HTTP Basic authentication")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 5*time.Minute {
		return nil, errors.New("timeout must be between 1 and 300 seconds")
	}

	dialTimeout := cfg.Timeout
	if dialTimeout > 10*time.Second {
		dialTimeout = 10 * time.Second
	}

	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			// This option is exposed explicitly in the UI for devices using a
			// self-signed certificate.
			InsecureSkipVerify: cfg.AllowInvalidTLS, // #nosec G402 -- user-selected local-device compatibility option
		},
	}

	return &APIClient{
		baseURL:  cloneURL(cfg.BaseURL),
		username: cfg.Username,
		password: cfg.Password,
		http: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
		transport: transport,
	}, nil
}

func (c *APIClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

func (c *APIClient) TestConnection(ctx context.Context) (int, error) {
	_, status, err := c.do(ctx, http.MethodGet, "status", nil)
	return status, err
}

func (c *APIClient) ReadAnalogOutputs(ctx context.Context) ([]AnalogOutput, error) {
	_, outputs, err := c.fetchSettings(ctx)
	return outputs, err
}

func (c *APIClient) UpdateAnalogOutputs(ctx context.Context, desired []AnalogOutput) ([]AnalogOutput, error) {
	if err := validateOutputs(desired); err != nil {
		return nil, err
	}

	// main.Settings has no required properties in the supplied Swagger schema.
	// Send only the field being changed so unrelated device settings, secrets,
	// and read-only values are never retransmitted by this utility.
	payload, err := buildAnalogOutputsPayload(desired)
	if err != nil {
		return nil, fmt.Errorf("could not build settings payload: %w", err)
	}
	if _, _, err := c.do(ctx, http.MethodPost, "settings", payload); err != nil {
		return nil, fmt.Errorf("settings update failed: %w", err)
	}

	// Read back from the device so that success means the requested values were
	// actually returned by the API after the write.
	_, actual, err := c.fetchSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings were posted, but verification read failed: %w", err)
	}
	if err := compareOutputs(desired, actual); err != nil {
		return actual, fmt.Errorf("settings were posted, but verification failed: %w", err)
	}
	return actual, nil
}

func (c *APIClient) fetchSettings(ctx context.Context) (settingsDocument, []AnalogOutput, error) {
	body, _, err := c.do(ctx, http.MethodGet, "settings", nil)
	if err != nil {
		return nil, nil, err
	}

	var document settingsDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, nil, fmt.Errorf("device returned invalid settings JSON: %w", err)
	}
	raw, ok := document["analogOutputs"]
	if !ok {
		return nil, nil, errors.New("settings JSON does not contain analogOutputs")
	}

	var outputs []AnalogOutput
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return nil, nil, fmt.Errorf("analogOutputs has an invalid format: %w", err)
	}
	if err := validateOutputs(outputs); err != nil {
		return nil, nil, fmt.Errorf("device returned invalid analogOutputs: %w", err)
	}
	return document, outputs, nil
}

func buildAnalogOutputsPayload(outputs []AnalogOutput) ([]byte, error) {
	if err := validateOutputs(outputs); err != nil {
		return nil, err
	}
	analogJSON, err := json.Marshal(outputs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(settingsDocument{"analogOutputs": analogJSON})
}

func validateOutputs(outputs []AnalogOutput) error {
	if len(outputs) != 2 {
		return fmt.Errorf("exactly 2 analog outputs are required; received %d", len(outputs))
	}
	for i, output := range outputs {
		if !isAllowedSource(output.Source) {
			return fmt.Errorf("analog output %d has unsupported source %q", i+1, output.Source)
		}
		if math.IsNaN(output.Low) || math.IsInf(output.Low, 0) {
			return fmt.Errorf("analog output %d low value is not finite", i+1)
		}
		if math.IsNaN(output.High) || math.IsInf(output.High, 0) {
			return fmt.Errorf("analog output %d high value is not finite", i+1)
		}
	}
	return nil
}

func compareOutputs(expected, actual []AnalogOutput) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("device returned %d outputs instead of %d", len(actual), len(expected))
	}
	for i := range expected {
		if expected[i].Source != actual[i].Source || expected[i].Log != actual[i].Log ||
			!nearlyEqual(expected[i].Low, actual[i].Low) || !nearlyEqual(expected[i].High, actual[i].High) {
			return fmt.Errorf("analog output %d differs from the requested value", i+1)
		}
	}
	return nil
}

func nearlyEqual(a, b float64) bool {
	difference := math.Abs(a - b)
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return difference <= 1e-9*scale
}

func isAllowedSource(source string) bool {
	for _, allowed := range allowedSources {
		if source == allowed {
			return true
		}
	}
	return false
}

func (c *APIClient) do(ctx context.Context, method, relativePath string, body []byte) ([]byte, int, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: relativePath})

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("could not read response: %w", err)
	}
	if len(responseBody) > maxResponseBodyBytes {
		return nil, resp.StatusCode, fmt.Errorf("response exceeded %s", byteCount(maxResponseBodyBytes))
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, &HTTPError{
			Method:     method,
			URL:        endpoint.String(),
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       responseSnippet(responseBody),
		}
	}
	return responseBody, resp.StatusCode, nil
}

func parseNumber(text string) (float64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("value is required")
	}
	// Accept a decimal comma when there is no decimal point. JSON serialization
	// remains locale-independent.
	if strings.Count(text, ",") == 1 && !strings.Contains(text, ".") {
		text = strings.Replace(text, ",", ".", 1)
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%q is not a finite number", text)
	}
	return value, nil
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func responseSnippet(body []byte) string {
	const limit = 4096
	text := strings.TrimSpace(string(body))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > limit {
		text = text[:limit] + "..."
	}
	return text
}

func byteCount(value int) string {
	if value%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", value>>20)
	}
	return fmt.Sprintf("%d bytes", value)
}

func friendlyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "The request timed out. Check the device address, network route, and timeout value."
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		detail := ""
		if httpErr.Body != "" {
			detail = "\n\nDevice response: " + httpErr.Body
		}
		switch httpErr.StatusCode {
		case 400:
			return "The device rejected the request as invalid (HTTP 400)." + detail
		case 401:
			return "Authentication failed (HTTP 401). Check the username and password." + detail
		case 403:
			return "The authenticated account is not allowed to perform this operation (HTTP 403)." + detail
		case 404:
			return "The API endpoint was not found (HTTP 404). Check the address and confirm that the device exposes /api/settings and /api/status." + detail
		case 405:
			return "The device does not allow this HTTP method on the endpoint (HTTP 405)." + detail
		default:
			if httpErr.StatusCode >= 500 {
				return fmt.Sprintf("The device returned a server error (HTTP %d).", httpErr.StatusCode) + detail
			}
			return fmt.Sprintf("The device returned HTTP %d.", httpErr.StatusCode) + detail
		}
	}

	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "certificate signed by unknown authority") || strings.Contains(lower, "certificate is not trusted"):
		return "The HTTPS certificate is not trusted. Install a valid certificate or explicitly enable 'Allow an invalid HTTPS certificate' for this device.\n\nTechnical detail: " + text
	case strings.Contains(lower, "certificate") && strings.Contains(lower, "not valid for"):
		return "The HTTPS certificate does not match the device address. Use the certificate hostname or explicitly allow an invalid certificate.\n\nTechnical detail: " + text
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no connection could be made"):
		return "The device refused the connection. Check the IP address, port, protocol, and whether the API service is running.\n\nTechnical detail: " + text
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "name or service not known"):
		return "The device hostname could not be resolved. Check the address or use its IP address.\n\nTechnical detail: " + text
	case strings.Contains(lower, "server gave http response to https client"):
		return "The address uses HTTPS, but the device answered with plain HTTP. Change the address to http://.\n\nTechnical detail: " + text
	case strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "host is unreachable"):
		return "The device is not reachable from this computer. Check the network connection and routing.\n\nTechnical detail: " + text
	default:
		return text
	}
}
