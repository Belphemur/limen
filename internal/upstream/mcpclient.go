package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// DialAndInitialize opens an mcp-go client against mcpURL, prefers the
// StreamableHTTP transport, and transparently falls back to legacy SSE
// when the server replies with transport.ErrLegacySSEServer during
// Initialize. The returned client is already initialized and ready for
// ListTools / CallTool; the caller owns Close().
//
// Either headers or httpClient (or both) may be nil. headers are static
// per call; for per-request auth injection pass an *http.Client whose
// Transport mutates the request (the gateway's AuthInjectingTransport).
func DialAndInitialize(
	ctx context.Context,
	mcpURL string,
	headers map[string]string,
	httpClient *http.Client,
	timeout time.Duration,
	clientName, clientVersion string,
) (*client.Client, error) {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}

	probe := &probeTransport{base: httpClient}
	c, err := newStreamableClient(mcpURL, headers, probe.wrap(), timeout)
	if err != nil {
		return nil, fmt.Errorf("build streamable client: %w", err)
	}
	_, initErr := c.Initialize(ctx, initReq)
	if initErr == nil {
		return c, nil
	}
	_ = c.Close()

	// 401 means the upstream needs credentials we don't have. SSE
	// will hit the same wall — surface the auth error directly so the
	// admin sees actionable diagnostics instead of a misleading
	// "transport not supported" chain.
	var authErr *transport.AuthorizationRequiredError
	if errors.As(initErr, &authErr) {
		return nil, fmt.Errorf("initialize: upstream requires authentication (HTTP 401)%s%s: %w", resourceMetadataHint(authErr), probe.diag(), initErr)
	}

	// Only fall back to legacy SSE on the documented sentinel. Other
	// errors (network, 5xx, 4xx-without-auth) should surface as-is.
	if !errors.Is(initErr, transport.ErrLegacySSEServer) {
		return nil, fmt.Errorf("initialize: %w%s", initErr, probe.diag())
	}

	sse, sseBuildErr := newSSEClient(mcpURL, headers, httpClient, timeout)
	if sseBuildErr != nil {
		// Preserve the original StreamableHTTP error in the chain so
		// the operator can see both failure modes.
		return nil, fmt.Errorf("initialize: streamable: %v%s; sse fallback build: %w", initErr, probe.diag(), sseBuildErr)
	}
	if _, sseInitErr := sse.Initialize(ctx, initReq); sseInitErr != nil {
		_ = sse.Close()
		return nil, fmt.Errorf("initialize: streamable: %v%s; sse fallback: %w", initErr, probe.diag(), sseInitErr)
	}
	return sse, nil
}

func resourceMetadataHint(err *transport.AuthorizationRequiredError) string {
	if err == nil || err.ResourceMetadataURL == "" {
		return ""
	}
	return fmt.Sprintf(" (resource_metadata=%s)", err.ResourceMetadataURL)
}

// probeTransport captures the status code and a snippet of the response
// body for the first non-2xx initialize POST so failures surface useful
// diagnostic context instead of mcp-go's generic "server returned 4xx"
// wrapper.
type probeTransport struct {
	base   *http.Client
	status int
	body   []byte
}

func (p *probeTransport) wrap() *http.Client {
	rt := http.DefaultTransport
	if p.base != nil && p.base.Transport != nil {
		rt = p.base.Transport
	}
	wrapped := &probeRoundTripper{p: p, base: rt}
	if p.base == nil {
		return &http.Client{Transport: wrapped}
	}
	c := *p.base
	c.Transport = wrapped
	return &c
}

func (p *probeTransport) diag() string {
	if p.status == 0 {
		return ""
	}
	if len(p.body) == 0 {
		return fmt.Sprintf(" [http %d]", p.status)
	}
	snippet := p.body
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return fmt.Sprintf(" [http %d body=%q]", p.status, snippet)
}

type probeRoundTripper struct {
	p    *probeTransport
	base http.RoundTripper
}

func (r *probeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if r.p.status == 0 && resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		r.p.status = resp.StatusCode
		r.p.body = buf
		resp.Body = io.NopCloser(bytes.NewReader(buf))
	}
	return resp, nil
}

func newStreamableClient(mcpURL string, headers map[string]string, httpClient *http.Client, timeout time.Duration) (*client.Client, error) {
	opts := []transport.StreamableHTTPCOption{transport.WithHTTPTimeout(timeout)}
	if len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}
	if httpClient != nil {
		opts = append(opts, transport.WithHTTPBasicClient(httpClient))
	}
	return client.NewStreamableHttpClient(mcpURL, opts...)
}

func newSSEClient(mcpURL string, headers map[string]string, httpClient *http.Client, timeout time.Duration) (*client.Client, error) {
	opts := []transport.ClientOption{}
	if len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}
	if httpClient != nil {
		opts = append(opts, transport.WithHTTPClient(httpClient))
	}
	if timeout > 0 {
		opts = append(opts, transport.WithEndpointTimeout(timeout))
		opts = append(opts, transport.WithResponseTimeout(timeout))
	}
	sseT, err := transport.NewSSE(mcpURL, opts...)
	if err != nil {
		return nil, err
	}
	c := client.NewClient(sseT)
	if err := c.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start sse client: %w", err)
	}
	return c, nil
}
