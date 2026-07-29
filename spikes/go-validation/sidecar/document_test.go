package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func losslessFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "lossless.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMutationPreservesUnknownRawValuesAndLargeNumbers(t *testing.T) {
	input := losslessFixture(t)
	before, err := parseJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	futureRootBefore, _ := before.get("futureRoot")
	threadsBefore, _ := before.get("threads")
	threadBefore := threadsBefore.items[0]
	futureThreadBefore, _ := threadBefore.get("futureThread")
	anchorBefore, _ := threadBefore.get("anchor")
	futureAnchorBefore, _ := anchorBefore.get("futureAnchor")

	document, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.SetStatus("thread_a", "handled"); err != nil {
		t.Fatal(err)
	}
	output, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(output) {
		t.Fatal("mutation output is not valid JSON")
	}
	if !bytes.Contains(output, []byte(`"status": "handled"`)) {
		t.Fatal("owned status field was not changed")
	}

	after, err := parseJSON(output)
	if err != nil {
		t.Fatal(err)
	}
	futureRootAfter, _ := after.get("futureRoot")
	threadsAfter, _ := after.get("threads")
	threadAfter := threadsAfter.items[0]
	futureThreadAfter, _ := threadAfter.get("futureThread")
	anchorAfter, _ := threadAfter.get("anchor")
	futureAnchorAfter, _ := anchorAfter.get("futureAnchor")

	assertBytesEqual(t, "root extension", futureRootBefore.raw, futureRootAfter.raw)
	assertBytesEqual(t, "thread extension", futureThreadBefore.raw, futureThreadAfter.raw)
	assertBytesEqual(t, "anchor extension", futureAnchorBefore.raw, futureAnchorAfter.raw)
	if !bytes.Contains(output, []byte("9007199254740993123456789")) {
		t.Fatal("large unknown integer lexeme was not preserved")
	}

	second, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	assertBytesEqual(t, "deterministic output", output, second)
}

func TestAppendMessageMutatesOnlyMessages(t *testing.T) {
	document, err := Decode(losslessFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := document.AppendMessage("thread_a", Message{
		ID:        "message_agent",
		Author:    "agent",
		Name:      "Codex",
		Body:      "Updated the explanation.",
		CreatedAt: time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	output, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"id": "message_agent"`,
		`"type":"agent"`,
		`"body": "Updated the explanation."`,
		`9007199254740993123456789`,
	} {
		if !bytes.Contains(output, []byte(required)) {
			t.Fatalf("output does not contain %s", required)
		}
	}
}

func TestRecursiveDuplicateKeysAreReadOnly(t *testing.T) {
	cases := []string{
		`{"schemaVersion":1,"schemaVersion":1,"threads":[]}`,
		`{"schemaVersion":1,"threads":[],"future":{"x":1,"x":2}}`,
		`{"schemaVersion":1,"threads":[],"future":[{"x":1,"x":2}]}`,
	}
	for _, input := range cases {
		_, err := Decode([]byte(input))
		if !errors.Is(err, ErrReadOnly) || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("Decode(%s) error = %v", input, err)
		}
	}
}

func TestDuplicateIDsAndUnsupportedSchemaAreReadOnly(t *testing.T) {
	duplicateThread := []byte(`{
		"schemaVersion":1,
		"threads":[
			{"id":"same","anchor":{"type":"document"},"messages":[]},
			{"id":"same","anchor":{"type":"document"},"messages":[]}
		]
	}`)
	if _, err := Decode(duplicateThread); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate thread error = %v", err)
	}

	duplicateMessage := []byte(`{
		"schemaVersion":1,
		"threads":[
			{"id":"a","anchor":{"type":"document"},"messages":[{"id":"same"}]},
			{"id":"b","anchor":{"type":"document"},"messages":[{"id":"same"}]}
		]
	}`)
	if _, err := Decode(duplicateMessage); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate message error = %v", err)
	}

	if _, err := Decode([]byte(`{"schemaVersion":2,"threads":[]}`)); !errors.Is(
		err,
		ErrUnsupportedSchema,
	) {
		t.Fatalf("unsupported schema error = %v", err)
	}
}

func TestKnownNumericFieldsUseExactIntegerValidation(t *testing.T) {
	valid := []byte(`{
		"schemaVersion":1,
		"threads":[{
			"id":"large",
			"anchor":{
				"type":"text",
				"range":{"start":9007199254740993,"end":9007199254740994}
			},
			"messages":[]
		}]
	}`)
	if _, err := Decode(valid); err != nil {
		t.Fatalf("large exact integer was rejected: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schemaVersion":1.0,"threads":[]}`),
		[]byte(`{
			"schemaVersion":1,
			"threads":[{
				"id":"bad",
				"anchor":{"type":"text","range":{"start":1.5,"end":2}},
				"messages":[]
			}]
		}`),
		[]byte(`{
			"schemaVersion":1,
			"threads":[{
				"id":"bad",
				"anchor":{"type":"text","range":{"start":-1,"end":2}},
				"messages":[]
			}]
		}`),
	} {
		if _, err := Decode(invalid); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("invalid numeric input error = %v", err)
		}
	}
}

func TestSizeLimitsApplyToReadAndMutation(t *testing.T) {
	oversized := bytes.Repeat([]byte(" "), MaxBytes+1)
	if _, err := Decode(oversized); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("oversized decode error = %v", err)
	}

	document, err := Decode([]byte(`{
		"schemaVersion":1,
		"threads":[{
			"id":"a",
			"anchor":{"type":"document"},
			"status":"open",
			"messages":[]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := document.AppendMessage("a", Message{
		ID:        "large",
		Author:    "human",
		Name:      "Reviewer",
		Body:      strings.Repeat("x", MaxBytes),
		CreatedAt: time.Unix(0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := document.Bytes(); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("oversized mutation error = %v", err)
	}
}

func FuzzDecodeDoesNotPanic(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":1,"threads":[]}`))
	f.Add(losslessFixtureForFuzz())
	f.Add([]byte(`{"schemaVersion":1,"threads":[],"x":{"duplicate":1,"duplicate":2}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxBytes+1 {
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

func losslessFixtureForFuzz() []byte {
	data, err := os.ReadFile(filepath.Join("testdata", "lossless.json"))
	if err != nil {
		panic(err)
	}
	return data
}

func assertBytesEqual(t *testing.T, name string, expected, actual []byte) {
	t.Helper()
	if !bytes.Equal(expected, actual) {
		t.Fatalf("%s changed\nexpected: %s\nactual:   %s", name, expected, actual)
	}
}
