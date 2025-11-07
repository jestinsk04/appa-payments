package helpers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// SameDay checks if two timestamps are on the same day.
func SameDay(t1, t2 time.Time) bool {
	if t1.Year() == t2.Year() && t1.Month() == t2.Month() && t1.Day() == t2.Day() {
		return true
	}
	return false
}

func GenerateAuthToken(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
