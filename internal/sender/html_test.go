package sender

import (
	"strings"
	"testing"
)

func TestHTMLToPlainText_Table(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty_input",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace_only_input",
			input:    "   \n\t  ",
			expected: "",
		},
		{
			name:     "strip_script_and_style_and_head",
			input:    "<html><head><title>Ignore</title><style>body { color: red; }</style></head><body><script>alert(1);</script><p>Clean content</p><noscript>No script</noscript><svg><circle/></svg></body></html>",
			expected: "Clean content",
		},
		{
			name:     "html_comment_removal",
			input:    "<p>Before <!-- ignore this comment --> After</p>",
			expected: "Before After",
		},
		{
			name:     "unclosed_comment_removal",
			input:    "<p>Start</p><!-- dangling comment without closing",
			expected: "Start",
		},
		{
			name:     "paragraphs_and_headings_linebreaks",
			input:    "<h1>Heading 1</h1><p>First paragraph.</p><h2>Heading 2</h2><p>Second paragraph.</p>",
			expected: "Heading 1\n\nFirst paragraph.\n\nHeading 2\n\nSecond paragraph.",
		},
		{
			name:     "line_breaks_and_divs",
			input:    "<div>First line<br/>Second line<br>Third line</div>",
			expected: "First line\nSecond line\nThird line",
		},
		{
			name:     "horizontal_rule",
			input:    "<p>Top</p><hr><p>Bottom</p>",
			expected: "Top\n\n---\n\nBottom",
		},
		{
			name:     "list_items_with_bullets",
			input:    "<ul><li>Alpha</li><li>Beta</li><li>Gamma</li></ul>",
			expected: "* Alpha\n* Beta\n* Gamma",
		},
		{
			name:     "table_rendering_cells_and_rows",
			input:    "<table><tr><th>IP Address</th><th>Status</th></tr><tr><td>192.0.2.1</td><td>Blocked</td></tr><tr><td>198.51.100.1</td><td>Allowed</td></tr></table>",
			expected: "IP Address | Status\n192.0.2.1 | Blocked\n198.51.100.1 | Allowed",
		},
		{
			name:     "nested_wrapper_table_and_inner_table",
			input:    `<table id="wrapper"><tr><td valign="top"><h2>Security Report</h2><table class="grid"><tr><th>Host</th><th>Result</th></tr><tr><td>node1.example.org</td><td>Blocked</td></tr></table><p>Summary done</p></td></tr></table>`,
			expected: "Security Report\n\nHost | Result\nnode1.example.org | Blocked\n\nSummary done",
		},
		{
			name:     "table_cell_inline_elements",
			input:    `<table><tr><td>October 5, 2026<br>10:00am</td><td><div>192.0.2.10</div><pre>Malicious Agent</pre></td></tr></table>`,
			expected: "October 5, 2026 - 10:00am | 192.0.2.10 - Malicious Agent",
		},
		{
			name:     "link_with_distinct_text",
			input:    `<p>Visit our <a href="https://example.com/login">Portal</a> for info.</p>`,
			expected: "Visit our Portal (https://example.com/login) for info.",
		},
		{
			name:     "link_with_matching_url_text",
			input:    `<p>Check <a href="https://example.org/test/">https://example.org/test</a> now.</p>`,
			expected: "Check https://example.org/test now.",
		},
		{
			name:     "link_with_empty_text",
			input:    `<p>Link: <a href="https://example.net/direct"></a></p>`,
			expected: "Link: https://example.net/direct",
		},
		{
			name:     "anchor_and_dangerous_scheme_links_skipped",
			input:    `<p><a href="#top">Back to top</a>, <a href="javascript:alert(1)">JS</a>, <a href="vbscript:msgbox(1)">VBS</a>, <a href="data:text/html;base64,PHNjcmlwdD4=">Data</a></p>`,
			expected: "Back to top, JS, VBS, Data",
		},
		{
			name:     "image_with_alt_text",
			input:    `<p>Photo: <img src="pic.jpg" alt="Security Shield" /></p>`,
			expected: "Photo: [Security Shield]",
		},
		{
			name:     "html_entity_decoding",
			input:    "<p>&quot;Alert&quot; &amp; &lt;warning&gt; &#39;test&#39; &nbsp; done</p>",
			expected: "\"Alert\" & <warning> 'test' done",
		},
		{
			name:     "collapse_consecutive_blank_lines",
			input:    "<p>First</p><br><br><br><br><p>Second</p>",
			expected: "First\n\nSecond",
		},
		{
			name:     "unquoted_attribute_and_tag_without_closing_bracket",
			input:    `<a href=https://sub.example.com>Unquoted</a> and <dangling tag`,
			expected: "Unquoted (https://sub.example.com) and",
		},
		{
			name:     "preformatted_block_outside_table",
			input:    "<p>Code sample:</p><pre>fmt.Println(\"ok\")</pre><p>Footer text</p>",
			expected: "Code sample:\n\nfmt.Println(\"ok\")\n\nFooter text",
		},
		{
			name:     "table_cell_starting_with_break",
			input:    "<table><tr><td><br>content</td></tr></table>",
			expected: "content",
		},
		{
			name:     "anchor_without_href_attribute",
			input:    `<a name="top">Anchor without link</a>`,
			expected: "Anchor without link",
		},
		{
			name:     "anchor_with_empty_href_attribute",
			input:    `<a href=>Anchor empty attribute</a>`,
			expected: "Anchor empty attribute",
		},
		{
			name:     "long_bullet_item_multiline_wrap",
			input:    "<ul><li>Alpha item with long description that goes well beyond the seventy eight character line limit to test indented line continuation for unordered list items</li></ul>",
			expected: "* Alpha item with long description that goes well beyond the seventy eight\n  character line limit to test indented line continuation for unordered list\n  items",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTMLToPlainText(tt.input)
			if got != tt.expected {
				t.Errorf("HTMLToPlainText() mismatch\nGot:\n%q\nWant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestHTMLToPlainText_WordWrap(t *testing.T) {
	longURL := "https://service.example.org/audit/report/incident/2026/security/investigation/deep/link"
	longText := "<p>This is a notification with a very long sentence that exceeds the standard seventy-eight character limit of the line wrap helper and includes a URL: " + longURL + " followed by additional explanations.</p>"

	res := HTMLToPlainText(longText)
	lines := strings.Split(res, "\n")

	foundURL := false
	for _, l := range lines {
		if strings.Contains(l, longURL) {
			foundURL = true
		}
		if len(l) > 78 && !strings.Contains(l, longURL) {
			t.Errorf("line exceeds 78 characters without containing long URL: %q (len %d)", l, len(l))
		}
	}
	if !foundURL {
		t.Errorf("expected unbroken URL %q in output: %s", longURL, res)
	}
}

func TestExtractTagName_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "tag_name_without_delimiters",
			input:    "customtag",
			expected: "customtag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTagName(tt.input)
			if got != tt.expected {
				t.Errorf("extractTagName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExtractAttr_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		expected string
	}{
		{
			name:     "unquoted_without_trailing_delimiters",
			tag:      "href=https://sub.example.org",
			expected: "https://sub.example.org",
		},
		{
			name:     "unclosed_quote_without_trailing_quote",
			tag:      `href="https://sub.example.net`,
			expected: "https://sub.example.net",
		},
		{
			name:     "empty_value_at_tag_end",
			tag:      "href=",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAttr(tt.tag, "href")
			if got != tt.expected {
				t.Errorf("extractAttr() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWrapSingleLine_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		maxCol   int
	}{
		{
			name:     "all_whitespace_exceeding_max_col",
			input:    strings.Repeat(" ", 80),
			expected: nil,
			maxCol:   78,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapSingleLine(tt.input, tt.maxCol)
			if len(got) != len(tt.expected) {
				t.Errorf("wrapSingleLine() = %v, want %v", got, tt.expected)
			}
		})
	}
}
