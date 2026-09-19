package emlx

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// applePlaceholderHeader marks an attachment part whose body Apple Mail did
// not write into the .partial.emlx. The attachment bytes live next to the
// Messages/ directory instead, under Attachments/<msg>/<part-index>/<name>.
const applePlaceholderHeader = "X-Apple-Content-Length:"

var (
	boundaryRe = regexp.MustCompile(`(?i)boundary="?([^";\s]+)"?`)
	filenameRe = regexp.MustCompile(`(?i)filename="?([^";]+)"?`)
)

// attachmentsDir returns Apple Mail's Attachments/<num> directory for the
// message at path, or "" when path is not a Messages/<num>.partial.emlx file
// or the directory does not exist.
func attachmentsDir(path string) string {
	base := filepath.Base(path)
	if !IsPartial(base) {
		return ""
	}
	num := strings.TrimSuffix(base, ".partial.emlx")
	if _, err := strconv.Atoi(num); err != nil {
		return ""
	}
	msgDir := filepath.Dir(path)
	if filepath.Base(msgDir) != "Messages" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(msgDir), "Attachments", num)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// restoreAttachments rewrites raw so that every attachment part carrying an
// X-Apple-Content-Length placeholder gets its body back from attDir.
// Parts whose file cannot be found are left as they are. All other bytes,
// including the message's line-ending style, are preserved.
func restoreAttachments(raw []byte, attDir string) ([]byte, int) {
	nl := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		nl = "\r\n"
	}
	lines := strings.Split(string(raw), nl)

	// Locate the top-level boundary in the message header.
	hdrEnd := indexBlank(lines, 0)
	if hdrEnd < 0 {
		return raw, 0
	}
	boundary := findBoundary(lines[:hdrEnd])
	if boundary == "" {
		return raw, 0
	}
	open, closeB := "--"+boundary, "--"+boundary+"--"

	var out []string
	restored := 0
	partIndex := 0
	i := 0
	for i < len(lines) {
		line := lines[i]
		if line != open {
			out = append(out, line)
			i++
			continue
		}
		// Start of a part: copy the boundary line, then read its header.
		partIndex++
		out = append(out, line)
		i++
		phEnd := indexBlank(lines, i)
		if phEnd < 0 {
			out = append(out, lines[i:]...)
			break
		}
		header := lines[i:phEnd]
		if !hasPlaceholder(header) {
			out = append(out, header...)
			i = phEnd
			continue
		}
		// Find the attachment file for this part.
		content, ok := readAttachment(attDir, strconv.Itoa(partIndex), findFilename(header))
		if !ok {
			out = append(out, header...)
			i = phEnd
			continue
		}
		// Emit the header without the placeholder, then the base64 body,
		// and skip the original (empty) body up to the next boundary line.
		for _, h := range header {
			if !strings.HasPrefix(h, applePlaceholderHeader) {
				out = append(out, h)
			}
		}
		out = append(out, "")
		out = append(out, base64Lines(content)...)
		i = phEnd + 1
		for i < len(lines) && lines[i] != open && lines[i] != closeB {
			i++
		}
		restored++
	}
	return []byte(strings.Join(out, nl)), restored
}

// indexBlank returns the index of the first empty line at or after from.
func indexBlank(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if lines[i] == "" {
			return i
		}
	}
	return -1
}

func hasPlaceholder(header []string) bool {
	for _, h := range header {
		if strings.HasPrefix(h, applePlaceholderHeader) {
			return true
		}
	}
	return false
}

// unfold joins folded header lines so regexps can match across continuations.
func unfold(header []string) string {
	var b strings.Builder
	for _, h := range header {
		if strings.HasPrefix(h, " ") || strings.HasPrefix(h, "\t") {
			b.WriteString(strings.TrimLeft(h, " \t"))
			continue
		}
		b.WriteString("\n")
		b.WriteString(h)
	}
	return b.String()
}

func findBoundary(header []string) string {
	for l := range strings.SplitSeq(unfold(header), "\n") {
		if !strings.HasPrefix(strings.ToLower(l), "content-type:") {
			continue
		}
		if m := boundaryRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}

func findFilename(header []string) string {
	if m := filenameRe.FindStringSubmatch(unfold(header)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// readAttachment returns the bytes of attDir/<partID>/<name>. When that exact
// file is absent but the part directory holds exactly one file, that file is
// used, since Apple Mail stores one file per part and may have decoded the
// name differently than the raw header spells it.
func readAttachment(attDir, partID, name string) ([]byte, bool) {
	dir := filepath.Join(attDir, partID)
	if name != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return b, true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var files []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e)
		}
	}
	if len(files) != 1 {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		return nil, false
	}
	return b, true
}

// base64Lines encodes b as RFC 2045 base64 with 76-character lines.
func base64Lines(b []byte) []string {
	enc := base64.StdEncoding.EncodeToString(b)
	var lines []string
	for len(enc) > 76 {
		lines = append(lines, enc[:76])
		enc = enc[76:]
	}
	return append(lines, enc)
}
