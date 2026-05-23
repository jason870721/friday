package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
)

// sign returns the HMAC SHA256 hex signature of the URL-encoded query string.
// params must already include timestamp (and recvWindow if desired).
func sign(params url.Values, secretKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(params.Encode()))
	return hex.EncodeToString(mac.Sum(nil))
}
