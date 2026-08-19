package main

// The `column -t -s $'\t'` half of every list command's pipeline.
//
// All four list commands end the same way: a jq template prints one
// tab-separated line per record and `column -t -s $'\t'` aligns them --
// pr list (forge line 412), issue list (938), label list (1499) and
// milestone list (1758). That is util-linux column, and this is its
// behaviour, verified against 2.41.4:
//
//   - cells are padded to the widest cell in their column plus a two-space
//     gap, and the last column is never padded;
//   - a row with fewer cells than the widest row still pays for the columns
//     it skipped, so a short row can end in trailing spaces;
//   - a line that is nothing but whitespace is dropped entirely;
//   - width is measured in display columns -- wcwidth -- not bytes or
//     characters.
//
// Because the separator work happens on the text, not on the fields, a tab or
// a newline inside a title (jq -r escapes neither) reaches column as a real
// separator and splits the row. Callers therefore render their fields to text
// with tabLines and hand the whole blob over, exactly as the pipeline does.
//
// One thing is deliberately not reproduced: with a terminal on standard output
// column narrows the table to the terminal's width. Redirected or piped -- how
// every test and every script sees it -- no narrowing happens, and that is
// what this renders.

import (
	"strings"
	"unicode"
)

// tabLines renders rows the way the jq -r template that feeds column does:
// tabs between the fields of a row, newlines between rows, and no trailing
// newline of its own (columnTable splits on "\n", so a trailing one would add
// an empty line -- which column would drop anyway, but the text should match
// what jq printed).
func tabLines(rows [][]string) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = strings.Join(row, "\t")
	}
	return strings.Join(lines, "\n")
}

// columnTable is `column -t -s $'\t'` over text. Empty input renders as empty
// output, the same as piping nothing into column.
func columnTable(text string) string {
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimLeft(line, " \t\v\f\r") == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}

	var widths []int
	for _, cells := range rows {
		for i, cell := range cells {
			w := cellWidth(cell)
			switch {
			case i == len(widths):
				widths = append(widths, w)
			case w > widths[i]:
				widths[i] = w
			}
		}
	}

	var b strings.Builder
	for _, cells := range rows {
		for i, cell := range cells {
			b.WriteString(cell)
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-cellWidth(cell)+2))
			}
		}
		// A short row still pays for every column it did not fill, except the
		// last one: "e\tf" in a four-column table ends in five spaces.
		for i := len(cells); i < len(widths)-1; i++ {
			b.WriteString(strings.Repeat(" ", widths[i]+2))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// cellWidth is the width column measures a cell at: wcwidth per character,
// summed. Combining marks and control characters take no space, East Asian
// wide and fullwidth characters take two, everything else takes one --
// including the ambiguous-width characters, which are narrow outside a CJK
// locale.
//
// The wide ranges below cover the CJK, kana, Hangul, fullwidth and emoji
// blocks. Rarer wide characters outside them (a handful of symbols, and the
// less common Tangut and Nushu blocks) are measured one column too narrow,
// which shows up as one space too many in the column after a title that
// contains one.
func cellWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case r < 0x20, r >= 0x7f && r < 0xa0:
			// wcwidth reports control characters as unprintable; column
			// prints them but gives them no width.
		case unicode.In(r, unicode.Mn, unicode.Me):
		case isWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

var wideRuneRanges = [...][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo initial consonants
	{0x2E80, 0x303E},   // CJK radicals, Kangxi, CJK symbols and punctuation
	{0x3041, 0x33FF},   // kana, Bopomofo, Hangul compatibility jamo, CJK compatibility
	{0x3400, 0x4DBF},   // CJK unified ideographs extension A
	{0x4E00, 0x9FFF},   // CJK unified ideographs
	{0xA000, 0xA4CF},   // Yi
	{0xA960, 0xA97F},   // Hangul Jamo extended-A
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility ideographs
	{0xFE10, 0xFE19},   // vertical forms
	{0xFE30, 0xFE6F},   // CJK compatibility forms, small form variants
	{0xFF01, 0xFF60},   // fullwidth forms
	{0xFFE0, 0xFFE6},   // fullwidth signs
	{0x17000, 0x18AFF}, // Tangut
	{0x1B000, 0x1B2FF}, // kana supplement, Nushu
	{0x1F300, 0x1F64F}, // emoji: symbols, pictographs, emoticons
	{0x1F680, 0x1F6FF}, // emoji: transport and map
	{0x1F900, 0x1F9FF}, // emoji: supplemental symbols and pictographs
	{0x20000, 0x3FFFD}, // CJK unified ideographs extensions B and beyond
}

func isWideRune(r rune) bool {
	for _, span := range wideRuneRanges {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	return false
}
