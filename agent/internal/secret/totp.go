package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTOTPDigits = 6
	defaultTOTPPeriod = 30 * time.Second
)

func GenerateTOTPSecret() (string, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}

func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(defaultTOTPDigits))
	q.Set("period", strconv.Itoa(int(defaultTOTPPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func VerifyTOTP(secret, code string, now time.Time, window int) bool {
	code = strings.TrimSpace(code)
	if len(code) != defaultTOTPDigits || secret == "" {
		return false
	}
	if window < 0 {
		window = 0
	}
	for drift := -window; drift <= window; drift++ {
		candidate, err := GenerateTOTPCode(secret, now.Add(time.Duration(drift)*defaultTOTPPeriod))
		if err == nil && hmac.Equal([]byte(candidate), []byte(code)) {
			return true
		}
	}
	return false
}

func GenerateTOTPCode(secret string, at time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	counter := uint64(at.Unix() / int64(defaultTOTPPeriod.Seconds()))
	var msg [8]byte
	for i := 7; i >= 0; i-- {
		msg[i] = byte(counter)
		counter >>= 8
	}
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binary := (uint32(sum[offset])&0x7f)<<24 |
		(uint32(sum[offset+1])&0xff)<<16 |
		(uint32(sum[offset+2])&0xff)<<8 |
		(uint32(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", binary%1000000), nil
}
