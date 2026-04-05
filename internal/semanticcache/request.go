package semanticcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type parsedRequest struct {
	Model       string
	Query       string
	ContextHash string
	Stream      bool
}

func ParseRequest(body []byte) (parsedRequest, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return parsedRequest{}, fmt.Errorf("invalid request payload")
	}

	stream, err := parseStreamFlag(payload)
	if err != nil {
		return parsedRequest{}, err
	}

	model := strings.TrimSpace(fmt.Sprint(payload["model"]))
	if model == "" || model == "<nil>" {
		return parsedRequest{}, fmt.Errorf("missing model")
	}

	messagesAny, ok := payload["messages"].([]any)
	if !ok || len(messagesAny) == 0 {
		return parsedRequest{}, fmt.Errorf("messages must be a non-empty array")
	}

	lastUserIdx := -1
	queryText := ""
	for i := len(messagesAny) - 1; i >= 0; i-- {
		messageMap, ok := messagesAny[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(fmt.Sprint(messageMap["role"]))
		if role != "user" {
			continue
		}
		queryText = extractMessageContent(messageMap["content"])
		lastUserIdx = i
		break
	}

	if strings.TrimSpace(queryText) == "" {
		return parsedRequest{}, fmt.Errorf("semantic cache requires a non-empty user prompt")
	}

	contextHash, err := buildContextHash(payload, messagesAny, lastUserIdx)
	if err != nil {
		return parsedRequest{}, err
	}

	return parsedRequest{
		Model:       model,
		Query:       queryText,
		ContextHash: contextHash,
		Stream:      stream,
	}, nil
}

func parseStreamFlag(payload map[string]any) (bool, error) {
	v, ok := payload["stream"]
	if !ok {
		return false, nil
	}
	if v == nil {
		return false, nil
	}

	stream, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("stream must be a boolean")
	}

	return stream, nil
}

func buildContextHash(payload map[string]any, messages []any, queryIdx int) (string, error) {
	context := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		if key == "stream" || key == "messages" {
			continue
		}
		context[key] = value
	}

	maskedMessages := make([]any, 0, len(messages))
	for idx, messageAny := range messages {
		messageMap, ok := messageAny.(map[string]any)
		if !ok {
			maskedMessages = append(maskedMessages, messageAny)
			continue
		}

		cloned := make(map[string]any, len(messageMap))
		for key, value := range messageMap {
			cloned[key] = value
		}

		if idx == queryIdx {
			cloned["content"] = "__semantic_query__"
		}

		maskedMessages = append(maskedMessages, cloned)
	}
	context["messages"] = maskedMessages

	encoded, err := json.Marshal(context)
	if err != nil {
		return "", fmt.Errorf("failed to hash semantic cache context")
	}

	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func extractMessageContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(itemMap["text"]))
			if text != "" && text != "<nil>" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return strings.TrimSpace(fmt.Sprint(content))
	}
}
