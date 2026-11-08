package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Exact JSON keys are checked recursively, including objects the policy reads.
// Distinct casing remains distinct; a TYPE field cannot shadow the exact type.
func uniqueJSON(dec *json.Decoder, depth int) error {
	if depth > 256 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err = uniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case json.Delim('['):
		for dec.More() {
			if err := uniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	}
	return nil
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func objects(raw json.RawMessage) []map[string]json.RawMessage {
	var value []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func stringValue(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func nonempty(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	return len(value) > 0 && !bytes.Equal(value, []byte("null")) && !bytes.Equal(value, []byte(`""`)) && !bytes.Equal(value, []byte("[]"))
}

// Inspect protocol-defined resource locations only. In particular, ordinary
// function JSON schemas and function arguments may legitimately name file_id.
func hasServerResources(fields map[string]json.RawMessage) bool {
	for _, message := range objects(fields["messages"]) {
		if referencedContent(message) {
			return true
		}
	}
	for _, item := range objects(fields["input"]) {
		if referencedPart(item) || referencedContent(item) {
			return true
		}
	}
	for _, tool := range objects(fields["tools"]) {
		if nonempty(tool["vector_store_ids"]) || nonempty(tool["file_ids"]) {
			return true
		}
		if raw, ok := tool["container"]; ok {
			if value := stringValue(raw); value != "" && value != "auto" {
				return true
			}
			container := object(raw)
			if nonempty(container["id"]) || nonempty(container["container_id"]) || nonempty(container["file_ids"]) {
				return true
			}
		}
	}
	return false
}

func referencedContent(item map[string]json.RawMessage) bool {
	for _, part := range objects(item["content"]) {
		if referencedPart(part) {
			return true
		}
	}
	return false
}

func referencedPart(part map[string]json.RawMessage) bool {
	if nonempty(part["file_id"]) || nonempty(part["container_id"]) {
		return true
	}
	source := object(part["source"])
	if stringValue(source["type"]) == "file" || nonempty(source["file_id"]) {
		return true
	}
	file := object(part["file"])
	return nonempty(file["file_id"])
}
