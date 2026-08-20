package upload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Name limits. The whole point of deriving a name is that the result is
// predictable on every filesystem, so both parts are capped well short of the
// 255-byte component limit that ext4, APFS and SMB share, leaving room for the
// separator, the extension and a uniqueness suffix.
const (
	maxNamePart = 80
	maxBaseName = 170
)

// SafeName turns arbitrary text - a title, an author, a chapter - into one
// filesystem path component.
//
// The client's own filename is never used for this: a browser will happily
// send "../../etc/cron.d/x" or a name that differs only by an invisible
// codepoint, and a library that is also an SMB share has to survive both. The
// name is folded to ASCII where an obvious folding exists, everything a
// filesystem reserves is dropped, and the result is trimmed of the leading and
// trailing characters (dots, spaces) that Windows silently rewrites.
func SafeName(s string) string {
	// NFD splits an accented letter into its base plus a combining mark, so
	// dropping the marks leaves a readable ASCII skeleton.
	decomposed := norm.NFD.String(s)

	var b strings.Builder
	lastSpace := false
	for _, r := range decomposed {
		switch {
		case unicode.Is(unicode.Mn, r):
			// a combining mark left over from the decomposition
			continue
		case r == ' ' || unicode.IsSpace(r):
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			continue
		case r > 0x7f:
			// Nothing sensible to fold it to; keep it only if it is a letter
			// or digit, so scripts without a Latin form still read correctly.
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
				lastSpace = false
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}

	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ". ")
	return truncateRunes(out, maxNamePart)
}

// truncateRunes cuts a string to at most n runes, on a word boundary when one
// is close enough that the result still reads as a name.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := strings.TrimRight(string(r[:n]), " ")
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.Trim(cut, ". ")
}

// BookBase builds the "<Author> - <Title>" stem an item is filed under. Either
// half may be missing; the fallback is used when both are.
func BookBase(author, title, fallback string) string {
	a, t := SafeName(author), SafeName(title)
	var base string
	switch {
	case a != "" && t != "":
		base = a + " - " + t
	case t != "":
		base = t
	case a != "":
		base = a
	default:
		base = SafeName(fallback)
	}
	if base == "" {
		base = "Untitled"
	}
	if len(base) > maxBaseName {
		base = truncateRunes(base, maxBaseName)
	}
	return base
}

// TrackName builds "<NN> - <label>" for one file of a multi-file audiobook.
// The index is zero-padded to the width of the largest index in the set, so a
// plain alphabetical listing of the directory is already in play order.
func TrackName(index, total int, label, ext string) string {
	width := 2
	if total > 99 {
		width = 3
	}
	name := SafeName(label)
	if name == "" {
		name = "Part"
	}
	return fmt.Sprintf("%0*d - %s%s", width, index, name, ext)
}

// SafeSubdir validates the optional folder an upload is filed into. It is one
// plain name, not a path: allowing a path here would put the caller in charge
// of where the bytes land, which is exactly what this endpoint must not do.
func SafeSubdir(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, `/\`) || strings.Contains(s, "\x00") {
		return "", fmt.Errorf("%w: it must not contain a path separator", ErrSubdir)
	}
	clean := SafeName(s)
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("%w: %q leaves nothing usable", ErrSubdir, printable([]byte(s)))
	}
	return clean, nil
}

// uniqueName returns a name inside dir that is not taken, appending " (2)",
// " (3)" and so on to base until it finds one. dir is created if missing.
//
// The check is a stat rather than a create: two uploads racing for the same
// title is possible but rare, and the loser gets a scan that finds one book
// with the other's bytes rather than a corrupted file, because the final step
// is a rename that either happens or does not.
func uniqueName(dir, base, ext string) (string, error) {
	for i := 1; i <= 999; i++ {
		name := base + ext
		if i > 1 {
			name = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("upload: too many books already filed as %q", base)
}
