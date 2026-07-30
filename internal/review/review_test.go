package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mdreview.dev/mdreview/internal/limits"
)

func TestDecodeAndAppendEmitsCanonicalTypedJSON(t *testing.T) {
	document, err := Decode([]byte(`{"schemaVersion":1,"threads":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	thread := testDocumentThread("thread_new", "message_new", "New review.")
	if err := document.AppendThread(thread); err != nil {
		t.Fatal(err)
	}
	output, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(output, []byte("\n")) {
		t.Fatal("output must end with a newline")
	}
	if !bytes.HasPrefix(output, []byte("{\n  \"schemaVersion\": 1,")) {
		t.Fatalf("output is not indented JSON: %s", output)
	}
	second, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	assertBytesEqual(t, "deterministic output", output, second)
}

func TestDecodeRejectsRecursiveDuplicateKeysAndDuplicateIDs(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"duplicate root key", `{"schemaVersion":1,"schemaVersion":1,"threads":[]}`},
		{"duplicate nested key", validSidecar(`{"id":"thread","anchor":{"type":"text","range":{"start":0,"start":0,"end":1},"source":"x","text":"x"},"status":"open","messages":[` + validMessage("message") + `]}`)},
		{"duplicate key in array", validSidecar(`{"id":"thread","anchor":{"type":"document"},"status":"open","messages":[{"id":"message","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"2026-07-28T12:00:00Z","createdAt":"2026-07-28T12:00:00Z"}]}`)},
		{
			"escaped duplicate key",
			validSidecar(`{"id":"thread","anchor":{"type":"text","range":{"start":0,"end":1},"source":"x","text":"x"},"status":"open","messages":[{"id":"message","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"2026-07-28T12:00:00Z","\u0063reatedAt":"2026-07-28T12:00:00Z"}]}`),
		},
		{
			"duplicate thread ID",
			validSidecar(
				`{"id":"same","anchor":{"type":"document"},"status":"open","messages":[` +
					validMessage("one") + `]},` +
					`{"id":"same","anchor":{"type":"document"},"status":"open","messages":[` +
					validMessage("two") + `]}`,
			),
		},
		{
			"duplicate message ID across threads",
			validSidecar(
				`{"id":"one","anchor":{"type":"document"},"status":"open","messages":[` +
					validMessage("same") + `]},` +
					`{"id":"two","anchor":{"type":"document"},"status":"open","messages":[` +
					validMessage("same") + `]}`,
			),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode([]byte(test.input)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Decode error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodeValidatesCompleteSchemaVersionOneSemantics(t *testing.T) {
	validTextThread := `{"id":"thread","anchor":{"type":"text","range":{"start":0,"end":1},` +
		`"source":"x","text":"x"},"status":"handled","messages":[` + validMessage("message") + `]}`
	if _, err := Decode([]byte(validSidecar(validTextThread))); err != nil {
		t.Fatalf("valid sidecar rejected: %v", err)
	}

	cases := []struct {
		name        string
		input       string
		unsupported bool
	}{
		{"root not object", `[]`, false},
		{"missing schema", `{"threads":[]}`, false},
		{"schema string", `{"schemaVersion":"1","threads":[]}`, false},
		{"schema decimal", `{"schemaVersion":1.0,"threads":[]}`, false},
		{"schema exponent", `{"schemaVersion":1e0,"threads":[]}`, false},
		{"negative schema", `{"schemaVersion":-1,"threads":[]}`, false},
		{"unsupported schema", `{"schemaVersion":2,"threads":[]}`, true},
		{"missing threads", `{"schemaVersion":1}`, false},
		{"threads not array", `{"schemaVersion":1,"threads":{}}`, false},
		{"unknown root field", `{"schemaVersion":1,"threads":[],"future":true}`, false},
		{"unknown thread field", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[` + validMessage("m") + `],"future":true}`), false},
		{"case-mismatched field", `{"schemaVersion":1,"Threads":[]}`, false},
		{"multiple JSON values", `{"schemaVersion":1,"threads":[]} {}`, false},
		{"thread not object", validSidecar(`[]`), false},
		{"missing thread ID", validSidecar(`{"anchor":{"type":"document"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"empty thread ID", validSidecar(`{"id":"","anchor":{"type":"document"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"missing anchor", validSidecar(`{"id":"t","status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"unknown anchor", validSidecar(`{"id":"t","anchor":{"type":"line"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"text missing range", validSidecar(`{"id":"t","anchor":{"type":"text","source":"x","text":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"range decimal", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":0.0,"end":1},"source":"x","text":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"range negative", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":-1,"end":1},"source":"x","text":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"range overflow", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":0,"end":18446744073709551616},"source":"x","text":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"range reversed", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":2,"end":1},"source":"x","text":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"empty source", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":0,"end":0},"source":"","text":""},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"missing visible text", validSidecar(`{"id":"t","anchor":{"type":"text","range":{"start":0,"end":1},"source":"x"},"status":"open","messages":[` + validMessage("m") + `]}`), false},
		{"invalid status", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"done","messages":[` + validMessage("m") + `]}`), false},
		{"empty messages", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[]}`), false},
		{"message not object", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[1]}`), false},
		{"empty message ID", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[` + validMessage("") + `]}`), false},
		{"missing author", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","body":"","createdAt":"2026-07-28T12:00:00Z"}]}`), false},
		{"invalid author type", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"bot","name":"Codex"},"body":"","createdAt":"2026-07-28T12:00:00Z"}]}`), false},
		{"empty author name", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"agent","name":""},"body":"","createdAt":"2026-07-28T12:00:00Z"}]}`), false},
		{"missing body", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"human","name":"Reviewer"},"createdAt":"2026-07-28T12:00:00Z"}]}`), false},
		{"invalid created timestamp", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"today"}]}`), false},
		{"non-UTC created timestamp", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"2026-07-28T13:00:00+01:00"}]}`), false},
		{"invalid edited timestamp", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"2026-07-28T12:00:00Z","editedAt":"later"}]}`), false},
		{"null edited timestamp", validSidecar(`{"id":"t","anchor":{"type":"document"},"status":"open","messages":[{"id":"m","author":{"type":"human","name":"Reviewer"},"body":"","createdAt":"2026-07-28T12:00:00Z","editedAt":null}]}`), false},
		{"document anchor extra known field", validSidecar(`{"id":"t","anchor":{"type":"document","text":""},"status":"open","messages":[` + validMessage("m") + `]}`), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.input))
			if test.unsupported {
				if !errors.Is(err, ErrUnsupportedSchema) {
					t.Fatalf("Decode error = %v, want ErrUnsupportedSchema", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Decode error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodeAcceptsExactLargeIntegerRanges(t *testing.T) {
	input := validSidecar(
		`{"id":"large","anchor":{"type":"text","range":{` +
			`"start":9007199254740993,"end":9007199254740994},` +
			`"source":"x","text":"x"},"status":"open","messages":[` +
			validMessage("message") + `]}`,
	)
	document, err := Decode([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	rangeValue := document.Threads()[0].Anchor.Range
	if rangeValue.Start != 9007199254740993 || rangeValue.End != 9007199254740994 {
		t.Fatalf("range = %+v", rangeValue)
	}
}

func TestDecodeAndBytesEnforceLimits(t *testing.T) {
	if _, err := Decode(bytes.Repeat([]byte(" "), int(limits.MaxReviewSidecarBytes+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized Decode error = %v, want ErrTooLarge", err)
	}

	document := NewDocument()
	thread := testDocumentThread("thread_limit", "message_limit", strings.Repeat("x", int(limits.MaxPersistedMessageBodyBytes)))
	if err := document.AppendThread(thread); err != nil {
		t.Fatal(err)
	}
	if _, err := document.Bytes(); err != nil {
		t.Fatalf("maximum message body rejected: %v", err)
	}
	thread.Messages[0].Body += "x"
	if err := NewDocument().AppendThread(thread); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("oversized message error = %v, want ErrInvalidOperation", err)
	}

	oversizedExistingMessage := validSidecar(
		`{"id":"thread","anchor":{"type":"document"},"status":"open","messages":[` +
			`{"id":"message","author":{"type":"human","name":"Reviewer"},"body":` +
			quotedTestString(strings.Repeat("x", int(limits.MaxPersistedMessageBodyBytes+1))) +
			`,"createdAt":"2026-07-28T12:00:00Z"}]}`,
	)
	if _, err := Decode([]byte(oversizedExistingMessage)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized existing message error = %v, want ErrInvalid", err)
	}

	oversizedSource := strings.Repeat("x", int(limits.MaxTextAnchorSourceBytes+1))
	oversizedExistingSource := validSidecar(
		`{"id":"thread","anchor":{"type":"text","range":{"start":0,"end":` +
			fmt.Sprint(len(oversizedSource)) + `},"source":` + quotedTestString(oversizedSource) +
			`,"text":"x"},"status":"open","messages":[` + validMessage("message") + `]}`,
	)
	if _, err := Decode([]byte(oversizedExistingSource)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized existing source error = %v, want ErrInvalid", err)
	}
}

func TestPublishedSchemaAndSidecarFixturesMatchDomain(t *testing.T) {
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "schema", "review-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Schema               string `json:"$schema"`
		AdditionalProperties bool   `json:"additionalProperties"`
		Defs                 map[string]struct {
			Required             []string `json:"required"`
			AdditionalProperties bool     `json:"additionalProperties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("schema draft = %q", schema.Schema)
	}
	thread := schema.Defs["thread"]
	for _, required := range []string{"id", "anchor", "status", "messages"} {
		if !contains(thread.Required, required) {
			t.Fatalf("published thread schema does not require %q", required)
		}
	}
	if schema.AdditionalProperties {
		t.Fatal("published root schema permits unknown fields")
	}
	for name, definition := range schema.Defs {
		if definition.AdditionalProperties {
			t.Fatalf("published %s schema permits unknown fields", name)
		}
	}

	fixture, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m2", "sidecar.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(fixture)
	if err != nil {
		t.Fatalf("published sidecar fixture rejected: %v", err)
	}
	if len(document.Threads()) != 1 {
		t.Fatalf("published sidecar threads = %d, want 1", len(document.Threads()))
	}

	reviewFixture, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m2", "review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(reviewFixture, &snapshot); err != nil {
		t.Fatalf("decode published review response: %v", err)
	}
	if snapshot.Path != "README.md" ||
		!validRevision(snapshot.DocumentRevision) ||
		snapshot.ReviewRevision == nil ||
		len(snapshot.Threads) != 1 ||
		snapshot.Threads[0].Attachment.State != AttachmentAttached {
		t.Fatalf("published review response = %+v", snapshot)
	}

	createFixture, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m2", "create-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var createResult CreateThreadResult
	if err := json.Unmarshal(createFixture, &createResult); err != nil {
		t.Fatalf("decode published create response: %v", err)
	}
	if !validRevision(createResult.DocumentRevision) ||
		!validRevision(createResult.ReviewRevision) ||
		createResult.Thread.Attachment.State != AttachmentAttached {
		t.Fatalf("published create response = %+v", createResult)
	}
}

func FuzzDecodeDoesNotPanic(fuzz *testing.F) {
	fuzz.Add([]byte(`{"schemaVersion":1,"threads":[]}`))
	fuzz.Add([]byte(`{"schemaVersion":1,"threads":[],"x":{"duplicate":1,"duplicate":2}}`))
	fuzz.Fuzz(func(t *testing.T, data []byte) {
		if int64(len(data)) > limits.MaxReviewSidecarBytes+1 {
			t.Skip()
		}
		document, err := Decode(data)
		if err != nil {
			return
		}
		output, err := document.Bytes()
		if err != nil {
			t.Fatalf("valid decoded document could not be emitted: %v", err)
		}
		if !json.Valid(output) {
			t.Fatal("emitted output is not valid JSON")
		}
	})
}

func testDocumentThread(threadID, messageID, body string) Thread {
	return Thread{
		ID:     threadID,
		Anchor: Anchor{Type: AnchorDocument},
		Status: StatusOpen,
		Messages: []Message{{
			ID:        messageID,
			Author:    Author{Type: "human", Name: "Reviewer"},
			Body:      body,
			CreatedAt: time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC).Format(time.RFC3339),
		}},
	}
}

func validSidecar(threads string) string {
	return `{"schemaVersion":1,"threads":[` + threads + `]}`
}

func validMessage(id string) string {
	return `{"id":"` + id + `","author":{"type":"human","name":"Reviewer"},` +
		`"body":"","createdAt":"2026-07-28T12:00:00Z"}`
}

func quotedTestString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func assertBytesEqual(t *testing.T, name string, expected, actual []byte) {
	t.Helper()
	if !bytes.Equal(expected, actual) {
		t.Fatalf("%s changed\nexpected: %s\nactual:   %s", name, expected, actual)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
