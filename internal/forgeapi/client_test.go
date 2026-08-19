package forgeapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const testToken = "fake-token-for-tests"

// TestAuthHeaderInProcessOnly is the security-relevant test the bash suite
// could only approximate: its mock curl aborted when it saw an Authorization
// header in argv, because argv is world-readable in /proc. Here the header is
// set on an in-process request, so it can be observed arriving at the server
// while never appearing in any process argument vector.
func TestAuthHeaderInProcessOnly(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: testToken}
	if _, err := c.Do(Request{Method: "GET", Path: "/api/v1/user"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if want := "token " + testToken; got != want {
		t.Errorf("Authorization header mismatch (value withheld); got %d bytes, want %d", len(got), len(want))
	}

	// No subprocess is involved: nothing ran but this process, and this
	// process never had the token on its command line.
	for _, arg := range os.Args {
		if strings.Contains(arg, testToken) {
			t.Fatal("token leaked into the process argument vector")
		}
	}
}

func TestDoGETSendsNoBodyAndNoContentTypeWhenUnset(t *testing.T) {
	var (
		method      string
		contentType string
		hasCT       bool
		body        []byte
		length      int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, hasCT = r.Header["Content-Type"]
		contentType = r.Header.Get("Content-Type")
		body, _ = readAll(r)
		length = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: testToken}
	if _, err := c.Do(Request{Method: "GET", Path: "/api/v1/user"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if method != "GET" {
		t.Errorf("method = %q, want GET", method)
	}
	if hasCT {
		t.Errorf("Content-Type = %q, want header absent", contentType)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
	if length > 0 {
		t.Errorf("Content-Length = %d, want 0 or unset", length)
	}
}

func TestDoSendsBodyAndContentType(t *testing.T) {
	var (
		method      string
		contentType string
		body        []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		body, _ = readAll(r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	payload := []byte(`{"title":"x"}`)
	c := &Client{BaseURL: srv.URL, Token: testToken}
	resp, err := c.Do(Request{
		Method:      "POST",
		Path:        "/api/v1/repos/acme/widget/issues",
		ContentType: "application/json",
		Body:        payload,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if method != "POST" {
		t.Errorf("method = %q, want POST", method)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("body = %q, want %q", body, payload)
	}
	if resp.Status != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.Status)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("body = %q", resp.Body)
	}
}

// bash passes no -L, and api() treats every 3xx as a failure. The client must
// therefore hand the 3xx back rather than chasing Location.
func TestDoDoesNotFollowRedirects(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/api/v1/moved" {
			w.Header().Set("Location", "/api/v1/target")
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"followed":true}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: testToken}
	resp, err := c.Do(Request{Method: "GET", Path: "/api/v1/moved"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != http.StatusPermanentRedirect {
		t.Errorf("status = %d, want 308", resp.Status)
	}
	if hits != 1 {
		t.Errorf("server saw %d requests, want 1 (redirect must not be followed)", hits)
	}
}

// A supplied http.Client without a redirect policy must still not follow
// redirects, and must not be mutated by the client.
func TestDoSuppliedClientKeepsRedirectPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/api/v1/target")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	supplied := &http.Client{}
	c := &Client{BaseURL: srv.URL, Token: testToken, HTTP: supplied}
	resp, err := c.Do(Request{Method: "GET", Path: "/api/v1/moved"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.Status)
	}
	if supplied.CheckRedirect != nil {
		t.Error("supplied client was mutated")
	}
}

// bash hands curl a path it percent-encoded itself with jq @uri. The client
// must not re-encode or normalise it.
func TestDoPathAndQueryPassThroughVerbatim(t *testing.T) {
	var uri string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := "/api/v1/repos/acme/widget/milestones?state=all&name=July%20batch&limit=100"
	c := &Client{BaseURL: srv.URL, Token: testToken}
	if _, err := c.Do(Request{Method: "GET", Path: path}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if uri != path {
		t.Errorf("request URI = %q, want %q", uri, path)
	}
}

// Dropping curl's -f is the whole point of api(): a 4xx must arrive with its
// body intact and without an error.
func TestDoNon2xxIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"user should have a permission to write to a repo"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: testToken}
	resp, err := c.Do(Request{Method: "GET", Path: "/api/v1/repos/acme/widget/issues/403"})
	if err != nil {
		t.Fatalf("Do returned an error for a 403: %v", err)
	}
	if resp.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "permission to write") {
		t.Errorf("body = %q, want the server's own message", resp.Body)
	}
}

func TestDoTransportFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	c := &Client{BaseURL: base, Token: testToken}
	if _, err := c.Do(Request{Method: "GET", Path: "/api/v1/user"}); err == nil {
		t.Fatal("Do succeeded against a closed server")
	}
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
