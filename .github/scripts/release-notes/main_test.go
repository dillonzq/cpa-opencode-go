package main

import (
	"strings"
	"testing"
)

const changelog = `# v0.2.0

Second release.

## Fixes

- Fixed a thing.

# v0.1.0

First release.
`

func TestExtractSectionKeepsOnlyTheTaggedVersion(t *testing.T) {
	got, err := extractSection(changelog, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := "# v0.2.0\n\nSecond release.\n\n## Fixes\n\n- Fixed a thing.\n"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if strings.Contains(got, "v0.1.0") {
		t.Fatalf("body must not contain an older version: %q", got)
	}
}

func TestExtractSectionTrimsSurroundingBlankLines(t *testing.T) {
	content := "\n\n# v0.2.0\n\n\nBody.\n\n\n\n# v0.1.0\n\nOld.\n\n\n"
	got, err := extractSection(content, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# v0.2.0\n\nBody.\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestExtractSectionAcceptsCRLF(t *testing.T) {
	content := strings.ReplaceAll(changelog, "\n", "\r\n")
	got, err := extractSection(content, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# v0.2.0\n\nSecond release.\n\n## Fixes\n\n- Fixed a thing.\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestExtractSectionIgnoresHeadingsInsideFences(t *testing.T) {
	content := "# v0.2.0\n\nRun the tests:\n\n```bash\n# v0.1.0 is not a section here\n```\n\n~~~\n# v0.0.9 neither is this\n~~~\n\n## Notes\n\nDone.\n\n# v0.1.0\n\nOld.\n"
	got, err := extractSection(content, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := "# v0.2.0\n\nRun the tests:\n\n```bash\n# v0.1.0 is not a section here\n```\n\n~~~\n# v0.0.9 neither is this\n~~~\n\n## Notes\n\nDone.\n"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestExtractSectionKeepsTrailingNewlineExactlyOnce(t *testing.T) {
	got, err := extractSection(changelog, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, ".\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("body must end with exactly one newline: %q", got)
	}
}

func TestExtractSectionErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		tag     string
		want    string
	}{
		{
			name:    "missing tag",
			content: changelog,
			tag:     "v0.3.0",
			want:    `no "# v0.3.0" section`,
		},
		{
			name:    "tag is not the first section",
			content: changelog,
			tag:     "v0.1.0",
			want:    `must start with "# v0.1.0"`,
		},
		{
			name:    "empty section",
			content: "# v0.2.0\n\n\n# v0.1.0\n\nOld.\n",
			tag:     "v0.2.0",
			want:    "has no content",
		},
		{
			name:    "preamble before the section",
			content: "Not a changelog.\n\n# v0.2.0\n\nBody.\n",
			tag:     "v0.2.0",
			want:    "must start with",
		},
		{
			name:    "empty tag",
			content: changelog,
			tag:     "   ",
			want:    "tag is required",
		},
		{
			name:    "empty notes",
			content: "",
			tag:     "v0.2.0",
			want:    "no \"# v0.2.0\" section",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := extractSection(test.content, test.tag)
			if err == nil {
				t.Fatalf("extraction must fail, got %q", got)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestSplitSectionsIgnoresSubheadings(t *testing.T) {
	sections := splitSections(changelog)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if sections[0].heading != "# v0.2.0" || sections[1].heading != "# v0.1.0" {
		t.Fatalf("headings = %q, %q", sections[0].heading, sections[1].heading)
	}
}

func TestSplitSectionsHandlesLongerClosingFence(t *testing.T) {
	sections := splitSections("# v0.2.0\n\n````\n```\nstill fenced\n````\n\n# v0.1.0\n\nOld.\n")
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if !strings.Contains(strings.Join(sections[0].body, "\n"), "still fenced") {
		t.Fatalf("inner fence closed the block early: %q", sections[0].body)
	}
}
