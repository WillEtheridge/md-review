package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type valueKind uint8

const (
	nullValue valueKind = iota
	boolValue
	numberValue
	stringValueKind
	arrayValue
	objectValue
)

type member struct {
	key    string
	keyRaw []byte
	value  *value
}

// value retains every unowned JSON value exactly as it appeared in the
// sidecar. Containers are rebuilt only along a mutation path, so arbitrary
// number lexemes and nested extension values never pass through float64.
type value struct {
	kind    valueKind
	raw     []byte
	members []member
	items   []*value
	dirty   bool
}

type parser struct {
	data []byte
	at   int
}

func parseJSON(data []byte) (*value, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON")
	}
	// One owned backing copy lets every raw subtree use a slice instead of
	// recursively cloning container bytes. Deep valid JSON therefore remains
	// linear in input size while callers cannot mutate preserved lexemes.
	parser := parser{data: cloneBytes(data)}
	parser.skipSpace()
	result, err := parser.parseValue()
	if err != nil {
		return nil, err
	}
	parser.skipSpace()
	if parser.at != len(data) {
		return nil, fmt.Errorf("unexpected data at byte %d", parser.at)
	}
	return result, nil
}

func (parser *parser) parseValue() (*value, error) {
	parser.skipSpace()
	if parser.at >= len(parser.data) {
		return nil, errors.New("unexpected end of JSON")
	}
	start := parser.at
	switch parser.data[parser.at] {
	case '{':
		return parser.parseObject(start)
	case '[':
		return parser.parseArray(start)
	case '"':
		if err := parser.scanString(); err != nil {
			return nil, err
		}
		return &value{kind: stringValueKind, raw: parser.data[start:parser.at]}, nil
	case 't':
		return parser.literal(start, "true", boolValue)
	case 'f':
		return parser.literal(start, "false", boolValue)
	case 'n':
		return parser.literal(start, "null", nullValue)
	default:
		if err := parser.scanNumber(); err != nil {
			return nil, err
		}
		return &value{kind: numberValue, raw: parser.data[start:parser.at]}, nil
	}
}

func (parser *parser) parseObject(start int) (*value, error) {
	parser.at++
	result := &value{kind: objectValue}
	seen := make(map[string]struct{})
	parser.skipSpace()
	if parser.consume('}') {
		result.raw = parser.data[start:parser.at]
		return result, nil
	}

	for {
		parser.skipSpace()
		keyStart := parser.at
		if err := parser.scanString(); err != nil {
			return nil, fmt.Errorf("object key: %w", err)
		}
		keyRaw := parser.data[keyStart:parser.at]
		var key string
		if err := json.Unmarshal(keyRaw, &key); err != nil {
			return nil, fmt.Errorf("object key: %w", err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate object member %q", key)
		}
		seen[key] = struct{}{}
		parser.skipSpace()
		if !parser.consume(':') {
			return nil, fmt.Errorf("expected colon after object key at byte %d", parser.at)
		}
		child, err := parser.parseValue()
		if err != nil {
			return nil, err
		}
		result.members = append(result.members, member{key: key, keyRaw: keyRaw, value: child})
		parser.skipSpace()
		if parser.consume('}') {
			result.raw = parser.data[start:parser.at]
			return result, nil
		}
		if !parser.consume(',') {
			return nil, fmt.Errorf("expected comma at byte %d", parser.at)
		}
	}
}

func (parser *parser) parseArray(start int) (*value, error) {
	parser.at++
	result := &value{kind: arrayValue}
	parser.skipSpace()
	if parser.consume(']') {
		result.raw = parser.data[start:parser.at]
		return result, nil
	}

	for {
		child, err := parser.parseValue()
		if err != nil {
			return nil, err
		}
		result.items = append(result.items, child)
		parser.skipSpace()
		if parser.consume(']') {
			result.raw = parser.data[start:parser.at]
			return result, nil
		}
		if !parser.consume(',') {
			return nil, fmt.Errorf("expected comma at byte %d", parser.at)
		}
	}
}

func (parser *parser) scanString() error {
	if !parser.consume('"') {
		return fmt.Errorf("expected string at byte %d", parser.at)
	}
	for parser.at < len(parser.data) {
		switch parser.data[parser.at] {
		case '"':
			parser.at++
			return nil
		case '\\':
			parser.at += 2
		default:
			parser.at++
		}
	}
	return errors.New("unterminated string")
}

func (parser *parser) scanNumber() error {
	start := parser.at
	for parser.at < len(parser.data) {
		switch parser.data[parser.at] {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-', '+', '.', 'e', 'E':
			parser.at++
		default:
			if parser.at == start {
				return fmt.Errorf("expected value at byte %d", parser.at)
			}
			return nil
		}
	}
	if parser.at == start {
		return fmt.Errorf("expected value at byte %d", parser.at)
	}
	return nil
}

func (parser *parser) literal(start int, literal string, kind valueKind) (*value, error) {
	if !bytes.HasPrefix(parser.data[parser.at:], []byte(literal)) {
		return nil, fmt.Errorf("expected %s at byte %d", literal, parser.at)
	}
	parser.at += len(literal)
	return &value{kind: kind, raw: parser.data[start:parser.at]}, nil
}

func (parser *parser) consume(expected byte) bool {
	if parser.at < len(parser.data) && parser.data[parser.at] == expected {
		parser.at++
		return true
	}
	return false
}

func (parser *parser) skipSpace() {
	for parser.at < len(parser.data) {
		switch parser.data[parser.at] {
		case ' ', '\t', '\r', '\n':
			parser.at++
		default:
			return
		}
	}
}

func (value *value) get(name string) (*value, bool) {
	if value.kind != objectValue {
		return nil, false
	}
	for index := range value.members {
		if value.members[index].key == name {
			return value.members[index].value, true
		}
	}
	return nil, false
}

func (value *value) markDirty() {
	value.dirty = true
}

func (value *value) markTreeDirty() {
	value.dirty = true
	for index := range value.members {
		value.members[index].value.markTreeDirty()
	}
	for _, item := range value.items {
		item.markTreeDirty()
	}
}

func (value *value) bytes() ([]byte, error) {
	var output bytes.Buffer
	value.write(&output, 0)
	output.WriteByte('\n')
	result := output.Bytes()
	if !json.Valid(result) {
		return nil, errors.New("lossless writer produced invalid JSON")
	}
	return cloneBytes(result), nil
}

func (value *value) write(output *bytes.Buffer, depth int) {
	if !value.dirty {
		output.Write(value.raw)
		return
	}
	switch value.kind {
	case objectValue:
		output.WriteByte('{')
		for index := range value.members {
			if index > 0 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
			writeIndent(output, depth+1)
			output.Write(value.members[index].keyRaw)
			output.WriteString(": ")
			value.members[index].value.write(output, depth+1)
		}
		if len(value.members) > 0 {
			output.WriteByte('\n')
			writeIndent(output, depth)
		}
		output.WriteByte('}')
	case arrayValue:
		output.WriteByte('[')
		for index, item := range value.items {
			if index > 0 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
			writeIndent(output, depth+1)
			item.write(output, depth+1)
		}
		if len(value.items) > 0 {
			output.WriteByte('\n')
			writeIndent(output, depth)
		}
		output.WriteByte(']')
	default:
		output.Write(value.raw)
	}
}

func writeIndent(output *bytes.Buffer, depth int) {
	output.Write(bytes.Repeat([]byte("  "), depth))
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}
