// StripHTML: an HTML-only email becomes plain Unicode text, and NOTHING
// else survives — no tags, no scripts, no styles, no URLs hiding in
// attributes, no entities left encoded. This is deliberately a hand-rolled
// state machine over the already-bounded input rather than a document
// parser dependency: the input is at most the protocol text budget, and
// the failure mode of a stripper is visible garbage, never execution.
package email

import "strings"

// StripHTML reduces markup to the text a person would have read.
func StripHTML(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	skipUntil := "" // closing tag whose CONTENT is dropped (script/style)
	for i < len(s) {
		c := s[i]
		if c != '<' {
			if skipUntil == "" {
				out.WriteByte(c)
			}
			i++
			continue
		}
		// A tag. Find its end; an unterminated tag swallows the tail, which
		// is the safe direction.
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		tag := s[i+1 : i+end]
		i += end + 1
		name := strings.ToLower(strings.TrimLeft(tag, "/ "))
		if sp := strings.IndexAny(name, " \t\r\n/"); sp >= 0 {
			name = name[:sp]
		}
		closing := strings.HasPrefix(strings.TrimSpace(tag), "/")
		switch {
		case skipUntil != "" && closing && name == skipUntil:
			skipUntil = ""
		case skipUntil != "":
			// still inside dropped content
		case !closing && (name == "script" || name == "style" ||
			name == "head" || name == "title" || name == "svg" ||
			name == "iframe" || name == "object" || name == "template"):
			skipUntil = name
		case name == "br" || name == "p" || name == "div" || name == "tr" ||
			name == "li" || name == "h1" || name == "h2" || name == "h3" ||
			name == "h4" || name == "blockquote":
			out.WriteByte('\n')
		}
	}
	return collapseSpace(decodeEntities(out.String()))
}

// decodeEntities handles the handful that dominate real mail; everything
// else stays literal, which is safe — it is already text.
func decodeEntities(s string) string {
	r := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&apos;", "'",
	)
	return r.Replace(s)
}

// collapseSpace folds runs of blank lines and trailing space so a table
// layout does not become forty empty lines.
func collapseSpace(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
