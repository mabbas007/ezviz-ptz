package onvif

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"strings"
)

// authorized verifies the WS-Security UsernameToken, accepting both
// PasswordText and PasswordDigest. When no ONVIF credentials are configured
// every client is allowed, matching the Python bridge.
func (a *App) authorized(root *node) bool {
	if a.cfg.OnvifUser == "" && a.cfg.OnvifPassword == "" {
		return true
	}

	token := root.find("UsernameToken")
	if token == nil {
		slog.Warn("ONVIF request without WS-Security UsernameToken")
		return false
	}

	usernameElement := token.find("Username")
	username := ""
	if usernameElement != nil {
		username = usernameElement.Text
	}
	if !constantTimeEqual(username, a.cfg.OnvifUser) {
		slog.Warn("ONVIF username mismatch", "username", username)
		return false
	}

	passwordElement := token.find("Password")
	if passwordElement == nil {
		return false
	}
	provided := strings.TrimSpace(passwordElement.Text)
	if provided == "" {
		return false
	}

	passwordType, _ := passwordElement.attr("Type")
	expected := ""

	switch {
	case strings.HasSuffix(passwordType, passwordDigest):
		nonceElement := token.find("Nonce")
		createdElement := token.find("Created")
		if nonceElement == nil || createdElement == nil {
			slog.Warn("ONVIF digest auth without Nonce/Created")
			return false
		}
		nonceText := strings.TrimSpace(nonceElement.Text)
		created := strings.TrimSpace(createdElement.Text)
		if nonceText == "" || created == "" {
			slog.Warn("ONVIF digest auth with empty Nonce/Created")
			return false
		}
		nonce, err := decodeNonce(nonceText)
		if err != nil {
			return false
		}
		digest := sha1.Sum(append(append(nonce, created...), a.cfg.OnvifPassword...))
		expected = base64.StdEncoding.EncodeToString(digest[:])
	case strings.HasSuffix(passwordType, passwordText) || passwordType == "":
		expected = a.cfg.OnvifPassword
	default:
		slog.Warn("Unsupported ONVIF password type", "type", passwordType)
		return false
	}

	if !constantTimeEqual(provided, expected) {
		slog.Warn("ONVIF password mismatch")
		return false
	}
	return true
}

func decodeNonce(encoded string) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		return nonce, nil
	}
	return base64.RawStdEncoding.DecodeString(encoded)
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
