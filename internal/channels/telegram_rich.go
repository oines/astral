package channels

import (
	"encoding/json"
	"strings"
)

// Rich messages have a recursive text/block representation rather than the
// ordinary Message.text field. Preserve their readable body in IMStore and
// model observations, including lists, tables and disclosure summaries.
type telegramRichMessage struct {
	Blocks []telegramRichBlock `json:"blocks"`
}

type telegramRichBlock struct {
	Text    json.RawMessage     `json:"text"`
	Summary json.RawMessage     `json:"summary"`
	Caption json.RawMessage     `json:"caption"`
	Blocks  []telegramRichBlock `json:"blocks"`
	Items   []struct {
		Label  string              `json:"label"`
		Blocks []telegramRichBlock `json:"blocks"`
	} `json:"items"`
	Cells [][]struct {
		Text json.RawMessage `json:"text"`
	} `json:"cells"`
}

func telegramRichBlocks(blocks []telegramRichBlock) string {
	var parts []string
	for _, block := range blocks {
		for _, raw := range []json.RawMessage{block.Summary, block.Text} {
			if text := telegramRichText(raw); text != "" {
				parts = append(parts, text)
			}
		}
		if len(block.Blocks) > 0 {
			parts = append(parts, telegramRichBlocks(block.Blocks))
		}
		for _, item := range block.Items {
			parts = append(parts, strings.TrimSpace(item.Label+" "+telegramRichBlocks(item.Blocks)))
		}
		for _, row := range block.Cells {
			var cells []string
			for _, cell := range row {
				cells = append(cells, telegramRichText(cell.Text))
			}
			parts = append(parts, strings.Join(cells, " | "))
		}
		if text := telegramRichText(block.Caption); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func telegramRichText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var out strings.Builder
		for _, item := range list {
			out.WriteString(telegramRichText(item))
		}
		return out.String()
	}
	var styled struct {
		Text json.RawMessage `json:"text"`
		URL  string          `json:"url"`
	}
	_ = json.Unmarshal(raw, &styled)
	if len(styled.Text) == 0 {
		return ""
	}
	text = telegramRichText(styled.Text)
	if styled.URL != "" && styled.URL != text {
		text += " (" + styled.URL + ")"
	}
	return text
}
