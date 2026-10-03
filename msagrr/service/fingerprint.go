package service

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"msagrr/domain"
)

// DataFingerprint is a SHA-256 over the exact set of readings of a
// version (part, operator, trial, value, recorded_at). It is order
// independent, so relabeling/reordering levels is visible only when
// values change. The fingerprint binds every result to the data it was
// computed from.
func DataFingerprint(ms []*domain.Measurement) string {
	keys := make([]string, len(ms))
	for i, m := range ms {
		var b strings.Builder
		b.WriteString(m.Part)
		b.WriteByte(0x1f)
		b.WriteString(m.Operator)
		b.WriteByte(0x1f)
		b.WriteString(strconv.Itoa(m.Trial))
		b.WriteByte(0x1f)
		b.WriteString(strconv.FormatFloat(m.Value, 'g', -1, 64))
		b.WriteByte(0x1f)
		b.WriteString(m.RecordedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
		keys[i] = b.String()
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0x1e})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
