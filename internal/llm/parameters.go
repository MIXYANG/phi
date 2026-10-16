package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// RequestParameters is a JSON merge patch for provider request options.
// A null value removes a field; false and zero remain explicit values.
type RequestParameters map[string]any

// Validate protects protocol fields, conversation data and output limits from overrides.
func (p RequestParameters) Validate(api RouterType) error {
	_, err := p.validatedPatch(api)
	return err
}

// Encode once so validation and merging use the same snapshot, even with custom marshalers.
func (p RequestParameters) validatedPatch(api RouterType) (map[string]json.RawMessage, error) {
	if len(p) == 0 {
		return nil, nil
	}
	switch api {
	case "", OpenAI, OpenAIResponses, Anthropic, Gemini:
	default:
		return nil, fmt.Errorf(
			"request_parameters: unsupported API %q; use OpenAI, OpenAIResponses, Anthropic or Gemini",
			api,
		)
	}
	patch, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("request_parameters must contain JSON-compatible values: %w", err)
	}
	overlay, err := unmarshalObject(patch)
	if err != nil {
		return nil, err
	}
	for key, value := range overlay {
		switch key {
		case "model",
			"messages",
			"input",
			"system",
			"instructions",
			"contents",
			"systemInstruction",
			"system_instruction",
			"tools",
			"stream",
			"stream_options",
			"n",
			"max_tokens",
			"max_completion_tokens",
			"max_output_tokens",
			"previous_response_id",
			"conversation":
			return nil, fmt.Errorf("request_parameters.%s is managed by phi; remove this override", key)
		case "background":
			// Compaction requires a completed response; phi does not poll background jobs.
			if api == OpenAIResponses && bytes.Equal(bytes.TrimSpace(value), []byte("true")) {
				return nil, errors.New(
					"request_parameters.background=true is unsupported for OpenAIResponses; remove it or set it to false",
				)
			}
		case "extra_body":
			if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, errors.New(
					"request_parameters.extra_body is an SDK wrapper; move its fields directly under request_parameters",
				)
			}
		case "generation_config":
			// Alternate spellings could bypass the container carrying phi's output cap.
			return nil, errors.New(
				"request_parameters.generation_config: use generationConfig so options merge with phi's output limits",
			)
		case "generationConfig":
			// Replacing this container would erase Gemini's compaction cap.
			var options map[string]json.RawMessage
			if err := json.Unmarshal(value, &options); err != nil || options == nil {
				return nil, errors.New(
					"request_parameters.generationConfig must be an object; keep phi's output limits",
				)
			}
			for _, field := range []string{"maxOutputTokens", "max_output_tokens", "candidateCount", "candidate_count"} {
				if _, ok := options[field]; ok {
					return nil, fmt.Errorf(
						"request_parameters.generationConfig.%s is managed by phi; remove this override",
						field,
					)
				}
			}
		}
	}
	return overlay, nil
}

// MarshalRequest applies overrides after endpoint mapping, preserving untouched JSON numbers.
func MarshalRequest(request any, cfg ModelConfig) ([]byte, error) {
	overlay, err := cfg.RequestParameters.validatedPatch(cfg.API)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(overlay) == 0 {
		return body, nil
	}
	target, err := unmarshalObject(body)
	if err != nil {
		return nil, err
	}
	mergeObject(target, overlay)
	return json.Marshal(target)
}

func unmarshalObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage)
	}
	return obj, nil
}

// mergeObject follows RFC 7386: null deletes, objects merge, other values replace.
func mergeObject(target, overlay map[string]json.RawMessage) {
	for key, value := range overlay {
		value = bytes.TrimSpace(value)
		if bytes.Equal(value, []byte("null")) {
			delete(target, key)
			continue
		}
		if len(value) > 0 && value[0] == '{' {
			previous := bytes.TrimSpace(target[key])
			if len(previous) == 0 || previous[0] != '{' {
				previous = []byte("{}")
			}
			merged, err := unmarshalObject(previous)
			if err != nil {
				continue // previous is not an object; the overlay replaces it
			}
			overlayObj, err := unmarshalObject(value)
			if err != nil {
				continue
			}
			mergeObject(merged, overlayObj)
			target[key] = mustMarshal(merged)
			continue
		}
		target[key] = value
	}
}

func mustMarshal(obj map[string]json.RawMessage) json.RawMessage {
	data, err := json.Marshal(obj)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}
