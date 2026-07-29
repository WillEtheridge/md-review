package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

type member struct {
	key    string
	keyRaw []byte
	value  *value
}

type value struct {
	kind    kind
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
	p := parser{data: data}
	p.space()
	result, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.at != len(data) {
		return nil, fmt.Errorf("unexpected data at byte %d", p.at)
	}
	return result, nil
}

func (p *parser) parseValue() (*value, error) {
	p.space()
	if p.at >= len(p.data) {
		return nil, errors.New("unexpected end of JSON")
	}
	start := p.at
	switch p.data[p.at] {
	case '{':
		return p.parseObject(start)
	case '[':
		return p.parseArray(start)
	case '"':
		if err := p.scanString(); err != nil {
			return nil, err
		}
		return &value{kind: kindString, raw: clone(p.data[start:p.at])}, nil
	case 't':
		return p.literal(start, "true", kindBool)
	case 'f':
		return p.literal(start, "false", kindBool)
	case 'n':
		return p.literal(start, "null", kindNull)
	default:
		if err := p.scanNumber(); err != nil {
			return nil, err
		}
		return &value{kind: kindNumber, raw: clone(p.data[start:p.at])}, nil
	}
}

func (p *parser) parseObject(start int) (*value, error) {
	p.at++
	result := &value{kind: kindObject}
	seen := make(map[string]struct{})
	p.space()
	if p.consume('}') {
		result.raw = clone(p.data[start:p.at])
		return result, nil
	}

	for {
		p.space()
		keyStart := p.at
		if err := p.scanString(); err != nil {
			return nil, fmt.Errorf("object key: %w", err)
		}
		keyRaw := clone(p.data[keyStart:p.at])
		var key string
		if err := json.Unmarshal(keyRaw, &key); err != nil {
			return nil, fmt.Errorf("object key: %w", err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate object member %q", key)
		}
		seen[key] = struct{}{}
		p.space()
		if !p.consume(':') {
			return nil, fmt.Errorf("expected colon after object key at byte %d", p.at)
		}
		child, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		result.members = append(result.members, member{
			key:    key,
			keyRaw: keyRaw,
			value:  child,
		})
		p.space()
		if p.consume('}') {
			result.raw = clone(p.data[start:p.at])
			return result, nil
		}
		if !p.consume(',') {
			return nil, fmt.Errorf("expected comma at byte %d", p.at)
		}
	}
}

func (p *parser) parseArray(start int) (*value, error) {
	p.at++
	result := &value{kind: kindArray}
	p.space()
	if p.consume(']') {
		result.raw = clone(p.data[start:p.at])
		return result, nil
	}

	for {
		child, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		result.items = append(result.items, child)
		p.space()
		if p.consume(']') {
			result.raw = clone(p.data[start:p.at])
			return result, nil
		}
		if !p.consume(',') {
			return nil, fmt.Errorf("expected comma at byte %d", p.at)
		}
	}
}

func (p *parser) scanString() error {
	if !p.consume('"') {
		return fmt.Errorf("expected string at byte %d", p.at)
	}
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case '"':
			p.at++
			return nil
		case '\\':
			p.at += 2
		default:
			p.at++
		}
	}
	return errors.New("unterminated string")
}

func (p *parser) scanNumber() error {
	start := p.at
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-', '+', '.', 'e', 'E':
			p.at++
		default:
			if p.at == start {
				return fmt.Errorf("expected value at byte %d", p.at)
			}
			return nil
		}
	}
	if p.at == start {
		return fmt.Errorf("expected value at byte %d", p.at)
	}
	return nil
}

func (p *parser) literal(start int, literal string, k kind) (*value, error) {
	if !bytes.HasPrefix(p.data[p.at:], []byte(literal)) {
		return nil, fmt.Errorf("expected %s at byte %d", literal, p.at)
	}
	p.at += len(literal)
	return &value{kind: k, raw: clone(p.data[start:p.at])}, nil
}

func (p *parser) consume(expected byte) bool {
	if p.at < len(p.data) && p.data[p.at] == expected {
		p.at++
		return true
	}
	return false
}

func (p *parser) space() {
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case ' ', '\t', '\r', '\n':
			p.at++
		default:
			return
		}
	}
}

func clone(data []byte) []byte {
	return append([]byte(nil), data...)
}

func (v *value) get(name string) (*value, bool) {
	if v.kind != kindObject {
		return nil, false
	}
	for i := range v.members {
		if v.members[i].key == name {
			return v.members[i].value, true
		}
	}
	return nil, false
}

func (v *value) set(name string, replacement *value) bool {
	if v.kind != kindObject {
		return false
	}
	for i := range v.members {
		if v.members[i].key == name {
			v.members[i].value = replacement
			v.dirty = true
			return true
		}
	}
	keyRaw, _ := json.Marshal(name)
	v.members = append(v.members, member{
		key:    name,
		keyRaw: keyRaw,
		value:  replacement,
	})
	v.dirty = true
	return true
}

func stringValue(text string) *value {
	raw, _ := json.Marshal(text)
	return &value{kind: kindString, raw: raw, dirty: true}
}

func (v *value) markDirty() {
	v.dirty = true
}

func (v *value) bytes() ([]byte, error) {
	var output bytes.Buffer
	v.write(&output, 0)
	output.WriteByte('\n')
	result := output.Bytes()
	if !json.Valid(result) {
		return nil, errors.New("lossless writer produced invalid JSON")
	}
	return clone(result), nil
}

func (v *value) write(output *bytes.Buffer, depth int) {
	if !v.dirty {
		output.Write(v.raw)
		return
	}
	switch v.kind {
	case kindObject:
		output.WriteByte('{')
		for i := range v.members {
			if i > 0 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
			indent(output, depth+1)
			output.Write(v.members[i].keyRaw)
			output.WriteString(": ")
			v.members[i].value.write(output, depth+1)
		}
		if len(v.members) > 0 {
			output.WriteByte('\n')
			indent(output, depth)
		}
		output.WriteByte('}')
	case kindArray:
		output.WriteByte('[')
		for i := range v.items {
			if i > 0 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
			indent(output, depth+1)
			v.items[i].write(output, depth+1)
		}
		if len(v.items) > 0 {
			output.WriteByte('\n')
			indent(output, depth)
		}
		output.WriteByte(']')
	default:
		output.Write(v.raw)
	}
}

func indent(output *bytes.Buffer, depth int) {
	output.Write(bytes.Repeat([]byte("  "), depth))
}
