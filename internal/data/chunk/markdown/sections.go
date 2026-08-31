package markdown

import (
	"regexp"
	"strings"
)

// section is one heading-delimited slice of a document: the literal
// heading line (e.g. "## Installation"), its full breadcrumb path, and
// the body content strictly after it (up to the next heading or EOF).
// headingLine is "" for content appearing before any heading at all.
type section struct {
	headingPath string
	headingLine string
	body        string
}

// atxHeading matches an ATX heading line ("## Title", optionally with a
// trailing closing sequence of #s) — Setext headings (underlined with
// `===`/`---`) are intentionally not recognized, per the ticket's "not a
// smart semantic splitter" scope.
var atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// splitSections walks content line by line, splitting into one section
// per heading. Lines inside a fenced code block are never treated as
// headings, even if they start with '#' — a real, common case (e.g. a
// shell comment inside a ```bash fence).
func splitSections(content string) []section {
	lines := strings.Split(content, "\n")

	var sections []section
	var headingStack []string
	currentPath := ""
	currentHeadingLine := ""
	var current strings.Builder
	inFence := false
	fenceMarker := ""

	flush := func() {
		body := strings.TrimSpace(current.String())
		if body != "" || currentHeadingLine != "" {
			sections = append(sections, section{headingPath: currentPath, headingLine: currentHeadingLine, body: body})
		}
		current.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if marker := fenceMarkerOf(trimmed); marker != "" {
			if !inFence {
				inFence, fenceMarker = true, marker
			} else if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
			}
			current.WriteString(line)
			current.WriteString("\n")
			continue
		}

		if !inFence {
			if m := atxHeading.FindStringSubmatch(line); m != nil {
				flush()
				level := len(m[1])
				if level-1 > len(headingStack) {
					// A level skip — treat as one deeper than the
					// current stack, same defensive approach NORM-002
					// uses for the same real-world case.
					level = len(headingStack) + 1
				}
				headingStack = append(append([]string{}, headingStack[:level-1]...), m[2])
				currentPath = strings.Join(headingStack, " > ")
				currentHeadingLine = line
				continue
			}
		}

		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()

	return sections
}

// fenceMarkerOf returns the fence delimiter ("```" or "~~~") a trimmed
// line opens or closes with, or "" if it isn't a fence line.
func fenceMarkerOf(trimmed string) string {
	for _, marker := range [...]string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker
		}
	}
	return ""
}

// packSection returns headingLine+body as a single part if it fits
// within maxBytes. Otherwise it packs body's paragraphs (greedily, at
// blank-line boundaries) into as many parts as needed, and prepends
// headingLine to the *first* part only — never splitting the heading
// line into its own bare, bodyless part. A single paragraph that alone
// exceeds maxBytes is emitted as its own over-limit part rather than
// being cut mid-paragraph — an accepted edge case per the ticket.
func packSection(headingLine, body string, maxBytes int) []string {
	full := joinNonEmpty(headingLine, body)
	if len(full) <= maxBytes {
		return []string{full}
	}

	groups := packParagraphs(splitParagraphs(body), maxBytes)
	if len(groups) == 0 {
		return []string{full}
	}
	if headingLine != "" {
		groups[0] = joinNonEmpty(headingLine, groups[0])
	}
	return groups
}

// packParagraphs greedily packs paragraphs into groups up to maxBytes
// each, splitting only at paragraph (blank-line) boundaries.
func packParagraphs(paragraphs []string, maxBytes int) []string {
	var groups []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			groups = append(groups, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	for _, p := range paragraphs {
		candidateLen := current.Len()
		if candidateLen > 0 {
			candidateLen += 2 // "\n\n" separator
		}
		candidateLen += len(p)

		if candidateLen > maxBytes && current.Len() > 0 {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}
	flush()

	return groups
}

// splitParagraphs splits body on blank lines into non-empty,
// whitespace-trimmed paragraphs.
func splitParagraphs(body string) []string {
	raw := strings.Split(body, "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinNonEmpty joins a and b with a blank line, skipping either side if
// empty (avoids a stray leading "\n\n" when there's no heading line).
func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}
