package proto

import (
	"regexp"
	"strings"
)

var (
	authRe = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)\s*[:=]\s*(?:(?:basic|bearer|digest|negotiate)\s+)?\S+`)
	urlRe  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	credRe = regexp.MustCompile(`[^\s/:@"'<>]+:[^\s/@"'<>]+@`)
	keyRe  = regexp.MustCompile(`tskey-[A-Za-z0-9_-]+`)
)

// Scrub removes URLs, credentials, auth headers and key-looking tokens from s and trims it to max
// runes, so a log line or error detail can never carry a sign-in link.
func Scrub(s string, max int) string {
	s = authRe.ReplaceAllString(s, "$1: <redacted>")
	s = urlRe.ReplaceAllString(s, "<url>")
	s = credRe.ReplaceAllString(s, "<cred>@")
	s = keyRe.ReplaceAllString(s, "<key>")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max])
	}
	return s
}
