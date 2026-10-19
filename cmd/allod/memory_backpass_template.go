package main

// Prompt templates come from the public checkout's skill directory at run
// time, not embedded, so the skill stays the spec (allod/tools#276). These
// two functions are the whole of the filler: a single left-to-right pass
// that never re-scans an emitted value, so a trace containing the text
// "{{TRACE}}" or "&" reaches the model byte for byte.

import (
	"fmt"
	"strings"
)

// backpassSelfMarker is the first line every prompt this command sends must
// carry: allod trace distill drops a session whose first user message
// starts with it, so this command's own model calls are never re-analyzed.
const backpassSelfMarker = "<!-- memory-backpass:self -->"

func checkTemplateHeader(name, template string) error {
	first := template
	if idx := strings.IndexByte(template, '\n'); idx >= 0 {
		first = template[:idx]
	}
	if first != backpassSelfMarker {
		return fmt.Errorf("%s: first line is not %q", name, backpassSelfMarker)
	}
	return nil
}

// templatePlaceholders returns every {{NAME}} in template, in order of
// appearance, duplicates included.
func templatePlaceholders(template string) ([]string, error) {
	var names []string
	i := 0
	for {
		start := strings.Index(template[i:], "{{")
		if start < 0 {
			return names, nil
		}
		start += i
		end := strings.Index(template[start:], "}}")
		if end < 0 {
			return nil, fmt.Errorf("unterminated {{ at byte %d", start)
		}
		end += start
		names = append(names, template[start+2:end])
		i = end + 2
	}
}

// checkTemplatePlaceholders refuses before any model call if template names
// a placeholder fillTemplate does not know how to fill.
func checkTemplatePlaceholders(name, template string, known map[string]bool) error {
	names, err := templatePlaceholders(template)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for _, n := range names {
		if !known[n] {
			return fmt.Errorf("%s: unknown placeholder {{%s}}", name, n)
		}
	}
	return nil
}

// fillTemplate copies template text and, at each placeholder, emits the
// matching value verbatim without rescanning it: i always jumps past the
// inserted value, never back into it.
func fillTemplate(template string, values map[string]string) (string, error) {
	var b strings.Builder
	i := 0
	for {
		start := strings.Index(template[i:], "{{")
		if start < 0 {
			b.WriteString(template[i:])
			return b.String(), nil
		}
		start += i
		b.WriteString(template[i:start])
		end := strings.Index(template[start:], "}}")
		if end < 0 {
			return "", fmt.Errorf("unterminated {{ at byte %d", start)
		}
		end += start
		name := template[start+2 : end]
		value, ok := values[name]
		if !ok {
			return "", fmt.Errorf("unknown placeholder {{%s}}", name)
		}
		b.WriteString(value)
		i = end + 2
	}
}
