package main

// --json/--jq for `issue view` and `pr view` (allod/tools#190): the raw body
// round-trips byte-for-byte, exactly the requested fields are printed with no
// header or comments, and one API request is made -- never the comments or
// reviews endpoints. Plain (no --json) output for both commands is unchanged
// and already pinned by TestReadIssueView/TestReadPRView in read_test.go.

import "testing"

// --- Body round-trip: the reason this flag exists ---

func TestViewJSONBody(t *testing.T) {
	tests := []struct {
		name string
		body string // Go-escaped JSON string body, e.g. `line1\n\n` as literal backslash-n
		want string // expected stdout: the raw body plus jq -r's one trailing newline
	}{
		{"a body that ends in two newlines", `line1\n\n`, "line1\n\n\n"},
		{"a body containing CRLF", `line1\r\nline2`, "line1\r\nline2\n"},
		{"a body equal to -n", `-n`, "-n\n"},
	}

	for _, tt := range tests {
		t.Run("issue view/"+tt.name, func(t *testing.T) {
			srv := newRecordingServer(t, map[string]cannedResponse{
				"/api/v1/repos/acme/widget/issues/20": {Body: `{"body":"` + tt.body + `"}`},
			})
			useServer(t, srv)
			useToken(t, fakeToken)
			useNoInferRepo(t)

			out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "body", "--jq", ".body")
			if code != 0 || errText != "" {
				t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
			}
			if out != tt.want {
				t.Errorf("stdout = %q, want %q", out, tt.want)
			}
			if n := srv.count(); n != 1 {
				t.Errorf("request count = %d, want 1 (issue object only, no comments)", n)
			}
		})

		t.Run("pr view/"+tt.name, func(t *testing.T) {
			srv := newRecordingServer(t, map[string]cannedResponse{
				"/api/v1/repos/acme/widget/pulls/12": {Body: `{"body":"` + tt.body + `"}`},
			})
			useServer(t, srv)
			useToken(t, fakeToken)
			useNoInferRepo(t)

			out, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "12", "--json", "body", "--jq", ".body")
			if code != 0 || errText != "" {
				t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
			}
			if out != tt.want {
				t.Errorf("stdout = %q, want %q", out, tt.want)
			}
			if n := srv.count(); n != 1 {
				t.Errorf("request count = %d, want 1 (PR object only, no comments or reviews)", n)
			}
		})
	}
}

// --- Multi-value flag: comma-separated and repeated agree ---

func TestViewJSONFieldAccumulation(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/20": {Body: `{"number":20,"title":"Fix backup"}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out1, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "number,title")
	if code != 0 || errText != "" {
		t.Fatalf("comma-separated: got (%q, %q, %d), want success", out1, errText, code)
	}
	out2, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "number", "--json", "title")
	if code != 0 || errText != "" {
		t.Fatalf("repeated flag: got (%q, %q, %d), want success", out2, errText, code)
	}
	if out1 != out2 {
		t.Errorf("comma-separated = %q, repeated flag = %q, want equal", out1, out2)
	}
	if want := "{\"number\":20,\"title\":\"Fix backup\"}\n"; out1 != want {
		t.Errorf("stdout = %q, want %q", out1, want)
	}
}

// --- Object-shaped fields: author, labels, milestone, timestamps ---

func TestViewJSONObjectFields(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/issues/20": {Body: `{"number":20,"user":{"login":"bob"},"labels":[{"name":"bug"},{"name":"urgent"}],"milestone":{"title":"July batch"},"created_at":"2026-06-01T00:00:00Z"}`},
		"/api/v1/repos/acme/widget/issues/21": {Body: `{"number":21}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	t.Run("author, labels, milestone, and a timestamp", func(t *testing.T) {
		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "author,labels,milestone,createdAt")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		want := `{"author":{"login":"bob"},"createdAt":"2026-06-01T00:00:00Z","labels":[{"name":"bug"},{"name":"urgent"}],"milestone":{"title":"July batch"}}` + "\n"
		if out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("fields the API omits render null, body renders empty string", func(t *testing.T) {
		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "21", "--json", "author,labels,milestone,closedAt,body")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		want := `{"author":null,"body":"","closedAt":null,"labels":null,"milestone":null}` + "\n"
		if out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("--jq walks into an object field", func(t *testing.T) {
		out, errText, code := runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "author", "--jq", ".author.login")
		if code != 0 || errText != "" {
			t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
		}
		if out != "bob\n" {
			t.Errorf("stdout = %q, want %q", out, "bob\n")
		}
	})
}

// --- PR-only fields ---

func TestViewJSONPRFields(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{
		"/api/v1/repos/acme/widget/pulls/12": {Body: `{"number":12,"title":"Improve tool","head":{"ref":"topic"},"base":{"ref":"master"}}`},
	})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	out, errText, code := runForge(t, "-R", "acme/widget", "pr", "view", "12", "--json", "headRefName,baseRefName")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if want := "{\"baseRefName\":\"master\",\"headRefName\":\"topic\"}\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}

	out, errText, code = runForge(t, "-R", "acme/widget", "pr", "view", "12", "--json", "headRefName", "--jq", ".headRefName")
	if code != 0 || errText != "" {
		t.Fatalf("got (%q, %q, %d), want success", out, errText, code)
	}
	if out != "topic\n" {
		t.Errorf("stdout = %q, want %q", out, "topic\n")
	}

	// headRefName/baseRefName do not exist on issue view.
	_, errText, code = runForge(t, "-R", "acme/widget", "issue", "view", "20", "--json", "headRefName")
	wantErr := "forge: unknown --json field \"headRefName\"; valid fields: number, title, body, state, author, labels, milestone, url, createdAt, updatedAt, closedAt\n"
	if code == 0 || errText != wantErr {
		t.Errorf("got (%q, %d), want (%q, nonzero)", errText, code, wantErr)
	}
}

// --- Validation errors: no API request, exit non-zero, message names the fix ---

func TestViewJSONValidationErrors(t *testing.T) {
	srv := newRecordingServer(t, map[string]cannedResponse{})
	useServer(t, srv)
	useToken(t, fakeToken)
	useNoInferRepo(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"unknown field",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--json", "bogus"},
			"forge: unknown --json field \"bogus\"; valid fields: number, title, body, state, author, labels, milestone, url, createdAt, updatedAt, closedAt\n",
		},
		{
			"empty --json value",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--json", ""},
			"forge: --json requires at least one field; valid fields: number, title, body, state, author, labels, milestone, url, createdAt, updatedAt, closedAt\n",
		},
		{
			// appendCSVValues (forge line 292) mirrors `IFS=, read -r -a`: it
			// keeps only the first line and drops a trailing empty item, so a
			// value whose first line is empty passes the raw non-empty check
			// above but names zero fields once parsed.
			"--json value whose first line is empty",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--json", "\nbody"},
			"forge: --json requires at least one field; valid fields: number, title, body, state, author, labels, milestone, url, createdAt, updatedAt, closedAt\n",
		},
		{
			"--jq without --json",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--jq", ".body"},
			"forge: cannot use --jq without specifying --json\n",
		},
		{
			"unsupported --jq expression",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--json", "body", "--jq", ".body | length"},
			"forge: only simple field paths such as .body are supported for --jq\n",
		},
		{
			"--jq field not requested with --json",
			[]string{"-R", "acme/widget", "issue", "view", "20", "--json", "title", "--jq", ".body"},
			"forge: --jq field \"body\" was not requested with --json\n",
		},
		{
			"pr view: unknown field",
			[]string{"-R", "acme/widget", "pr", "view", "12", "--json", "bogus"},
			"forge: unknown --json field \"bogus\"; valid fields: number, title, body, state, author, labels, milestone, url, createdAt, updatedAt, closedAt, headRefName, baseRefName\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errText, code := runForge(t, tt.args...)
			if code == 0 {
				t.Errorf("exit code = 0, want nonzero")
			}
			if errText != tt.want {
				t.Errorf("stderr = %q, want %q", errText, tt.want)
			}
		})
	}

	if n := srv.count(); n != 0 {
		t.Errorf("validation errors made %d API requests, want 0", n)
	}
}
