package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func jsonLine(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func utcNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func normalizeCIK(cik string) string {
	digits := make([]byte, 0, len(cik))
	for i := 0; i < len(cik); i++ {
		if cik[i] >= '0' && cik[i] <= '9' {
			digits = append(digits, cik[i])
		}
	}
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return fmt.Sprintf("%010s", string(digits))
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustCanonicalJSON(value any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSpace(buf.String())
}

func stableID(prefix string, payload any) string {
	return prefix + "-" + sha256Hex([]byte(mustCanonicalJSON(payload)))
}

func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func nullableString(v any) any {
	switch t := v.(type) {
	case sql.NullString:
		if t.Valid {
			return t.String
		}
		return nil
	case string:
		if t == "" {
			return nil
		}
		return t
	default:
		return v
	}
}

func nullableInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullableFloat(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}

func parseDate(value string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", value, time.UTC)
}

func dateFromEpochDay(day int64) string {
	if day <= 0 {
		return ""
	}
	return time.Unix(day*86400, 0).UTC().Format("2006-01-02")
}

func utcFromEpochSecond(epochSecond int64) string {
	if epochSecond <= 0 {
		return ""
	}
	return time.Unix(epochSecond, 0).UTC().Format("2006-01-02T15:04:05.000Z")
}

func durationDays(start, end string) int {
	s, err1 := parseDate(start)
	e, err2 := parseDate(end)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(e.Sub(s).Hours()/24) + 1
}

func maxString(values ...string) string {
	out := ""
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	if out == "" {
		return utcNow()
	}
	return out
}
