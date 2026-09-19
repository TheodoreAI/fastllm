package llm

import (
	"strings"
	"unicode"
)

// Package-internal helpers for the channel-routed completion format used by
// several self-hosted servers, where a single stream carries both private
// reasoning ("to=self") and the user-facing answer ("to=user"), separated by
// <|message|> framing tokens. Nothing here is tied to a particular model: the
// format is detected from the bytes on the wire.

// HasChannelMarkers reports whether text carries channel routing headers, which is
// how this format is recognised without knowing which model produced it.
func HasChannelMarkers(s string) bool {
	return strings.Contains(s, "to=user") || strings.Contains(s, "to=self")
}

// CanonicalModelAlias collapses the aliases a server reports for one model onto the
// id the user configured for that endpoint. Servers commonly advertise several names
// for the same weights (a fully-qualified repo path, a short name, a size-suffixed
// name); without the configured id there is no way to know they are the same, so an
// empty configured id leaves the reported name untouched.
func CanonicalModelAlias(reported, configured string) string {
	if configured == "" {
		return reported
	}
	normalize := func(name string) string {
		lower := strings.ToLower(strings.TrimSpace(name))
		if slash := strings.LastIndexAny(lower, "/\\"); slash >= 0 {
			lower = lower[slash+1:]
		}
		return strings.ReplaceAll(lower, "_", "-")
	}
	left, right := normalize(reported), normalize(configured)
	if left == "" || right == "" {
		return reported
	}
	if strings.HasPrefix(left, right) || strings.HasPrefix(right, left) {
		return configured
	}
	return reported
}

var framingMarkers = []string{
	"<|start|>assistant",
	"<|start|>",
	"<|message|>",
	"<|eom|>",
	"<|eot|>",
}

// stripFramingMarkers removes any leftover ATEM / special token framing from text.
func stripFramingMarkers(s string) string {
	for _, m := range framingMarkers {
		s = strings.ReplaceAll(s, m, "")
	}
	return s
}

// SplitChannelContent parses a raw completion in the channel format, separating
// internal chain-of-thought (the "to=self" channel) from the user-facing answer
// (the "to=user" channel) and stripping all raw channel routing tokens.
func SplitChannelContent(raw string) (content string, reasoning string) {
	// Look for the user channel header.
	userIdx := strings.Index(raw, "to=user")
	if userIdx != -1 {
		afterUser := raw[userIdx+len("to=user"):]
		afterUser = strings.TrimPrefix(afterUser, "<|message|>")
		afterUser = stripFramingMarkers(afterUser)
		afterUser = strings.TrimLeft(afterUser, "\r\n")

		beforeUser := raw[:userIdx]
		selfIdx := strings.Index(beforeUser, "to=self")
		if selfIdx != -1 {
			reas := beforeUser[selfIdx+len("to=self"):]
			reas = strings.TrimPrefix(reas, "<|message|>")
			// Remove trailing assistant/transition markers
			reas = stripFramingMarkers(reas)
			reas = strings.TrimSuffix(strings.TrimSpace(reas), "assistant")
			return afterUser, strings.TrimSpace(reas)
		}
		return afterUser, ""
	}

	// If no to=user header exists, check if the entire output was reasoning.
	selfIdx := strings.Index(raw, "to=self")
	if selfIdx != -1 {
		reas := raw[selfIdx+len("to=self"):]
		reas = strings.TrimPrefix(reas, "<|message|>")
		reas = stripFramingMarkers(reas)
		return "", strings.TrimSpace(reas)
	}

	return raw, ""
}

type channelFilterState int

const (
	stateInit channelFilterState = iota
	stateReasoning
	stateContent
)

// channelStreamFilter buffers and demuxes streaming tokens in the channel format
// into separate reasoning and user-facing content token callbacks, stripping
// the raw "to=self" and "to=user" routing headers.
type channelStreamFilter struct {
	onToken     func(string)
	onReasoning func(string)

	state    channelFilterState
	buf      strings.Builder
	holdback strings.Builder
}

func newChannelStreamFilter(onToken func(string), onReasoning func(string)) *channelStreamFilter {
	return &channelStreamFilter{
		onToken:     onToken,
		onReasoning: onReasoning,
		state:       stateInit,
	}
}

// Feed receives the next incremental token string from the model stream.
func (f *channelStreamFilter) Feed(token string) {
	switch f.state {
	case stateInit:
		f.buf.WriteString(token)
		s := f.buf.String()

		if userIdx := strings.Index(s, "to=user"); userIdx != -1 {
			// Direct answer to user without reasoning
			afterUser := s[userIdx+len("to=user"):]
			afterUser = strings.TrimPrefix(afterUser, "<|message|>")
			afterUser = stripFramingMarkers(afterUser)
			afterUser = strings.TrimLeft(afterUser, "\r\n")

			f.state = stateContent
			f.buf.Reset()
			if afterUser != "" && f.onToken != nil {
				f.onToken(afterUser)
			}
			return
		}

		if selfIdx := strings.Index(s, "to=self"); selfIdx != -1 {
			// Reasoning started
			afterSelf := s[selfIdx+len("to=self"):]
			afterSelf = strings.TrimPrefix(afterSelf, "<|message|>")
			afterSelf = stripFramingMarkers(afterSelf)
			afterSelf = strings.TrimLeft(afterSelf, "\r\n")

			f.state = stateReasoning
			f.buf.Reset()
			if afterSelf != "" {
				f.feedReasoning(afterSelf)
			}
			return
		}

		// If buffer has grown past the size of any possible channel header (~32 bytes)
		// and has neither to=self nor to=user, it is standard content.
		if f.buf.Len() >= 32 {
			f.state = stateContent
			flush := stripFramingMarkers(f.buf.String())
			f.buf.Reset()
			if flush != "" && f.onToken != nil {
				f.onToken(flush)
			}
		}

	case stateReasoning:
		f.feedReasoning(token)

	case stateContent:
		cleaned := stripFramingMarkers(token)
		if cleaned != "" && f.onToken != nil {
			f.onToken(cleaned)
		}
	}
}

// feedReasoning processes reasoning tokens, holding back candidate transition
// suffixes so that "assistant to=user" transitions are not leaked to onReasoning.
func (f *channelStreamFilter) feedReasoning(token string) {
	f.holdback.WriteString(token)
	hb := f.holdback.String()

	if userIdx := strings.Index(hb, "to=user"); userIdx != -1 {
		// Transition to user content!
		beforeUser := hb[:userIdx]
		// Strip any trailing transition markers like "assistant", "<|start|>", "<|eom|>"
		beforeUser = stripFramingMarkers(beforeUser)
		beforeUser = strings.TrimSuffix(strings.TrimSpace(beforeUser), "assistant")

		if beforeUser != "" && f.onReasoning != nil {
			f.onReasoning(beforeUser)
		}

		afterUser := hb[userIdx+len("to=user"):]
		afterUser = strings.TrimPrefix(afterUser, "<|message|>")
		afterUser = stripFramingMarkers(afterUser)
		afterUser = strings.TrimLeft(afterUser, "\r\n")

		f.state = stateContent
		f.holdback.Reset()
		if afterUser != "" && f.onToken != nil {
			f.onToken(afterUser)
		}
		return
	}

	// If no to=user yet, check if hb ends with a possible prefix of "assistant to=user"
	// or "<|start|>assistant to=user"
	candidatePrefixes := []string{
		"<|", "<|eom", "<|start",
		"assistant", "assist", "ass",
		"to=", "to",
		"\nassistant", "\r\nassistant",
	}

	trimmedEnd := strings.TrimRightFunc(hb, unicode.IsSpace)
	needsHold := false
	for _, pfx := range candidatePrefixes {
		if strings.HasSuffix(trimmedEnd, pfx) || strings.HasSuffix(hb, pfx) {
			needsHold = true
			break
		}
	}

	if !needsHold {
		// Safe to emit accumulated reasoning tokens
		clean := stripFramingMarkers(hb)
		if clean != "" && f.onReasoning != nil {
			f.onReasoning(clean)
		}
		f.holdback.Reset()
	}
}

// Flush emits any remaining buffered text when the stream terminates.
func (f *channelStreamFilter) Flush() {
	switch f.state {
	case stateInit:
		s := stripFramingMarkers(f.buf.String())
		f.buf.Reset()
		if s != "" && f.onToken != nil {
			f.onToken(s)
		}
	case stateReasoning:
		s := stripFramingMarkers(f.holdback.String())
		f.holdback.Reset()
		if s != "" && f.onReasoning != nil {
			f.onReasoning(s)
		}
	case stateContent:
		s := stripFramingMarkers(f.holdback.String())
		f.holdback.Reset()
		if s != "" && f.onToken != nil {
			f.onToken(s)
		}
	}
}
