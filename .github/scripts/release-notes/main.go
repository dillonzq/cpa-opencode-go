// Command release-notes extracts the section for a release tag from
// RELEASE_NOTES.md, so the published release body contains only that version.
//
// RELEASE_NOTES.md is a changelog: every version keeps its own "# vX.Y.Z"
// section, newest first. The release tag must match the first section, which is
// what this command validates before writing the body.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	notesPath := flag.String("notes", "", "path to RELEASE_NOTES.md")
	tag := flag.String("tag", "", "release tag whose section becomes the release body, such as v0.1.1")
	outPath := flag.String("out", "", "path to write the extracted notes to")
	flag.Parse()

	if *notesPath == "" || *tag == "" || *outPath == "" {
		fatalf("notes, tag, and out are required")
	}
	data, errRead := os.ReadFile(*notesPath)
	if errRead != nil {
		fatalf("read notes: %v", errRead)
	}
	section, errExtract := extractSection(string(data), *tag)
	if errExtract != nil {
		fatalf("%v", errExtract)
	}
	if errWrite := os.WriteFile(*outPath, []byte(section), 0o644); errWrite != nil {
		fatalf("write notes: %v", errWrite)
	}
	fmt.Printf("release body for %s: %d bytes\n", *tag, len(section))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

type section struct {
	heading string
	body    []string
}

// extractSection returns "# <tag>" and its content as the release body. The
// tag section must be the first one in the file, so an unreleased section can
// never be published by mistake.
func extractSection(content, tag string) (string, error) {
	trimmedTag := strings.TrimSpace(tag)
	if trimmedTag == "" {
		return "", errors.New("tag is required")
	}
	heading := "# " + trimmedTag
	sections := splitSections(content)
	if !hasSection(sections, heading) {
		return "", fmt.Errorf("no %q section in the notes", heading)
	}
	if first := firstNonBlankLine(content); first != heading {
		return "", fmt.Errorf("the notes must start with %q, found %q", heading, first)
	}
	body := trimBlankLines(sections[0].body)
	if len(body) == 0 {
		return "", fmt.Errorf("section %q has no content", heading)
	}
	return heading + "\n\n" + strings.Join(body, "\n") + "\n", nil
}

// splitSections groups the content by level-1 headings. Headings inside fenced
// code blocks are content, not section boundaries, so a shell comment such as
// "# v0.1.0" in an example cannot split a release section.
func splitSections(content string) []section {
	var sections []section
	current := -1
	fence := ""
	for _, line := range splitLines(content) {
		trimmed := strings.TrimSpace(line)
		if fence == "" {
			if marker := fenceMarker(trimmed); marker != "" {
				fence = marker
			} else if heading, ok := sectionHeading(trimmed); ok {
				sections = append(sections, section{heading: heading})
				current = len(sections) - 1
				continue
			}
		} else if closesFence(trimmed, fence) {
			fence = ""
		}
		if current >= 0 {
			sections[current].body = append(sections[current].body, line)
		}
	}
	return sections
}

func hasSection(sections []section, heading string) bool {
	for _, candidate := range sections {
		if candidate.heading == heading {
			return true
		}
	}
	return false
}

// sectionHeading reports whether a trimmed line is a level-1 heading.
func sectionHeading(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "# ") {
		return "", false
	}
	heading := strings.TrimRight(trimmed, " \t")
	if heading == "#" {
		return "", false
	}
	return heading, true
}

// fenceMarker returns the opening fence sequence when the line starts a fenced
// code block, and "" otherwise.
func fenceMarker(trimmed string) string {
	for _, marker := range []byte{'`', '~'} {
		length := 0
		for length < len(trimmed) && trimmed[length] == marker {
			length++
		}
		if length >= 3 {
			return strings.Repeat(string(marker), length)
		}
	}
	return ""
}

// closesFence reports whether the line closes the fence opened by marker. A
// closing fence uses the same character and is at least as long as the opening.
func closesFence(trimmed, fence string) bool {
	if !strings.HasPrefix(trimmed, fence) {
		return false
	}
	return strings.Trim(trimmed, fence[:1]) == ""
}

func splitLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimBlankLines(lines []string) []string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end]
}

func firstNonBlankLine(content string) string {
	for _, line := range splitLines(content) {
		if strings.TrimSpace(line) != "" {
			return strings.TrimRight(strings.TrimSpace(line), " \t")
		}
	}
	return ""
}
