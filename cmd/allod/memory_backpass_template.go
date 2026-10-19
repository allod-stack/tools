package main

// Templates are read from the public checkout at run time, so the skill
// stays the spec.

import (
	"fmt"
	"strings"
)

// Every prompt this command sends opens with this line: allod trace distill
// drops a session whose first user message starts with it.
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

// A single left-to-right pass that never rescans an emitted value: a trace
// containing the text {{TRACE}} or & must reach the model byte for byte.
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
