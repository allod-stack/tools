// Package forgeapi is a thin HTTP client for the Forgejo REST API.
//
// It transliterates the curl invocations of the bash forge script and nothing
// more: it builds the URL by concatenation, sets the Authorization header in
// this process, sends a body only when one is given, and never follows
// redirects. Status interpretation, error text and exit codes belong to the
// caller (cmd/forge), because those are the observable contract.
//
// The bash script had to hand curl the token through a --config file so it
// never appeared in an argument vector that other users could read in /proc.
// This package has no argument vector: the header is set on an in-process
// http.Request. The token is never logged, never wrapped into an error, and
// never leaves this file except as the outgoing Authorization header.
package forgeapi

import (
	"bytes"
	"io"
	"net/http"
)

// Client talks to one Forgejo instance.
type Client struct {
	// BaseURL is the instance root with no trailing slash, e.g.
	// "https://forge.anarch.diy". Request.Path is appended to it verbatim,
	// exactly as bash builds "$FORGE_URL/api/v1$path".
	BaseURL string

	// Token is sent as "Authorization: token <Token>" on every request,
	// mirroring the curl --config file bash writes. Callers validate it
	// before constructing the client; an empty token still sends the header,
	// which is what the curl config file did.
	Token string

	// HTTP is optional. When nil, a shared client that does not follow
	// redirects is used. A supplied client is used as-is when it already
	// defines a redirect policy, and otherwise gets the non-following one:
	// bash passes no -L, and treating a 3xx as success turns an http:// base
	// URL into a silent empty answer.
	HTTP *http.Client
}

// Request is one API call.
type Request struct {
	// Method is the HTTP method, matching curl -X.
	Method string

	// Path is already URL-encoded and starts at the API root, e.g.
	// "/api/v1/repos/owner/repo/issues?state=open". It is appended to
	// BaseURL without normalisation so percent-escapes survive intact.
	Path string

	// ContentType is sent as the Content-Type header when non-empty. bash's
	// api() passes -H "Content-Type: application/json" on every call it makes,
	// including GETs with no body; its token-verification curl passes no such
	// header at all. That asymmetry is observable on the wire, so it is the
	// caller's to declare.
	ContentType string

	// Body is sent as the request body when non-nil, matching curl -d. A nil
	// body sends no body and no Content-Length, matching a curl call without
	// -d.
	Body []byte
}

// Response is the status and the raw body of one API call. The body is
// returned verbatim; trimming, splitting and error rendering are the caller's
// job, because bash does them in api() rather than in curl.
type Response struct {
	Status int
	Body   []byte
}

// noRedirect matches curl without -L: the 3xx response itself is the answer.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

var defaultHTTPClient = &http.Client{CheckRedirect: noRedirect}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return defaultHTTPClient
	}
	if c.HTTP.CheckRedirect != nil {
		return c.HTTP
	}
	clone := *c.HTTP
	clone.CheckRedirect = noRedirect
	return &clone
}

// Do sends one request and returns its status and body. A non-2xx status is
// not an error here: bash deliberately drops curl's -f so the response body
// survives, and the caller renders it.
func (c *Client) Do(req Request) (Response, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequest(req.Method, c.BaseURL+req.Path, body)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Authorization", "token "+c.Token)
	if req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	}

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{Status: resp.StatusCode}, err
	}
	return Response{Status: resp.StatusCode, Body: data}, nil
}
