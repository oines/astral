package httpcontract

import (
	"reflect"
	"slices"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestObservationQuoteContractIsOptionalAndPreservesPositionUnits(t *testing.T) {
	contract := New()
	contract.Describe(Operation{Method: "GET", Path: "/observation", Response: reflect.TypeFor[domain.Observation]()})
	observation := contract.Schemas["astral_internal_domain_Observation"].(map[string]any)
	properties := observation["properties"].(map[string]any)
	quoteRef := properties["reply_quote"].(map[string]any)
	if quoteRef["$ref"] != "#/components/schemas/astral_internal_domain_TextQuote" || slices.Contains(observation["required"].([]string), "reply_quote") {
		t.Fatal("quote was omitted from the API contract or made mandatory for ordinary messages")
	}
	replyAttachments := properties["reply_attachments"].(map[string]any)
	if replyAttachments["type"] != "array" || replyAttachments["items"].(map[string]any)["$ref"] != "#/components/schemas/astral_internal_domain_Attachment" || slices.Contains(observation["required"].([]string), "reply_attachments") {
		t.Fatal("referenced media was omitted, made mandatory, or lost the attachment contract")
	}
	quote := contract.Schemas["astral_internal_domain_TextQuote"].(map[string]any)
	fields := quote["properties"].(map[string]any)
	position := fields["position"].(map[string]any)
	if position["type"] != "integer" || position["description"] != "Approximate sender-specified offset in UTF-16 code units of the original source message; text is authoritative." || fields["manual"].(map[string]any)["type"] != "boolean" {
		t.Fatal("quote API contract lost its UTF-16 position or manual flag")
	}
	for _, field := range []string{"text", "position", "manual"} {
		if !slices.Contains(quote["required"].([]string), field) {
			t.Fatalf("present quote does not require %s", field)
		}
	}
}
