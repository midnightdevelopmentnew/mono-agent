// Package chromecookies reads and decrypts cookies directly from a Chrome
// (or Chromium/Brave/Edge) profile's own cookie database on disk, rather
// than via the DevTools/CDP protocol. This exists specifically so a login
// browser can run with zero automation-capable flags at all — no
// --remote-debugging-port, nothing distinguishing it from a browser the
// user launched by hand — since sites like Google's sign-in flow appear to
// detect the mere presence of a remote debugging port rather than anything
// about how it's used.
package chromecookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	_ "modernc.org/sqlite"
)

// Cookie mirrors proto.NetworkCookieParam's JSON shape (name, value, domain,
// path, secure, httpOnly, expires), which is what crawler_sessions.cookies_json
// already stores and what run.go's session-restore unmarshals into — so no
// downstream code needs to change to consume cookies read this way.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

// keychainService maps a browser's binary path to the macOS Keychain
// "Safe Storage" service/account names it stores its cookie-encryption
// password under.
func keychainService(chromePath string) (service, account string) {
	switch {
	case strings.Contains(chromePath, "Chromium"):
		return "Chromium Safe Storage", "Chromium"
	case strings.Contains(chromePath, "Brave"):
		return "Brave Safe Storage", "Brave"
	case strings.Contains(chromePath, "Edge"):
		return "Microsoft Edge Safe Storage", "Microsoft Edge"
	default:
		return "Chrome Safe Storage", "Chrome"
	}
}

// derivedKey fetches this browser's Keychain-stored password (macOS will
// prompt the user to approve access the first time) and derives its
// AES-128 cookie-encryption key the same way Chromium's OSCrypt does:
// PBKDF2-HMAC-SHA1(password, salt="saltysalt", iterations=1003, 16 bytes).
func derivedKey(chromePath string) ([]byte, error) {
	service, account := keychainService(chromePath)
	out, err := exec.Command("security", "find-generic-password", "-w", "-s", service, "-a", account).Output()
	if err != nil {
		return nil, fmt.Errorf("reading %q from Keychain (approve the system permission prompt if one appeared): %w", service, err)
	}
	password := bytes.TrimRight(out, "\n")
	return pbkdf2.Key(password, []byte("saltysalt"), 1003, 16, sha1.New), nil
}

// decrypt reverses Chromium's macOS cookie encryption: AES-128-CBC with a
// static 16-space IV, after stripping the "v10"/"v11" version prefix.
func decrypt(encrypted, key []byte) (string, error) {
	if len(encrypted) < 3 {
		return "", fmt.Errorf("encrypted value too short")
	}
	switch string(encrypted[:3]) {
	case "v10", "v11":
	default:
		return "", fmt.Errorf("unsupported cookie encryption version %q", encrypted[:3])
	}
	ciphertext := encrypted[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext is not a non-zero multiple of the block size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	padLen := int(plaintext[len(plaintext)-1])
	if padLen <= 0 || padLen > aes.BlockSize || padLen > len(plaintext) {
		return "", fmt.Errorf("invalid padding")
	}
	return string(plaintext[:len(plaintext)-padLen]), nil
}

// cookiesDBPath returns the path to a Chrome profile's cookie SQLite
// database, checking both the modern (Network/Cookies) and legacy
// (Cookies) locations.
func cookiesDBPath(userDataDir string) (string, error) {
	for _, rel := range []string{
		filepath.Join("Default", "Network", "Cookies"),
		filepath.Join("Default", "Cookies"),
	} {
		p := filepath.Join(userDataDir, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no cookies database found under %s — has the browser loaded any page yet?", userDataDir)
}

// chromeEpochToUnix converts a Chrome/WebKit timestamp (microseconds since
// 1601-01-01) to Unix seconds.
func chromeEpochToUnix(v int64) float64 {
	if v == 0 {
		return 0
	}
	const epochDiffSeconds = 11644473600 // seconds between 1601-01-01 and 1970-01-01
	return float64(v)/1_000_000 - epochDiffSeconds
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ReadCookies reads and decrypts every cookie stored for domain (and its
// subdomains) from the given Chrome profile directory. Chrome keeps its
// live Cookies file open (WAL mode) while running, so this copies it to a
// temp file first rather than opening it in place.
func ReadCookies(chromePath, userDataDir, domain string) ([]Cookie, error) {
	src, err := cookiesDBPath(userDataDir)
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp("", "monoagent-cookies-*.sqlite")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := copyFile(src, tmpPath); err != nil {
		return nil, fmt.Errorf("copying cookies database: %w", err)
	}

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	key, err := derivedKey(chromePath)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(
		`SELECT name, value, encrypted_value, host_key, path, expires_utc, is_secure, is_httponly
		 FROM cookies WHERE host_key LIKE ?`,
		"%"+domain,
	)
	if err != nil {
		return nil, fmt.Errorf("querying cookies: %w", err)
	}
	defer rows.Close()

	var cookies []Cookie
	for rows.Next() {
		var (
			name, value, hostKey, path string
			encryptedValue             []byte
			expiresUTC                 int64
			isSecure, isHTTPOnly       int
		)
		if err := rows.Scan(&name, &value, &encryptedValue, &hostKey, &path, &expiresUTC, &isSecure, &isHTTPOnly); err != nil {
			continue
		}
		if value == "" && len(encryptedValue) > 0 {
			dec, decErr := decrypt(encryptedValue, key)
			if decErr != nil {
				continue // skip cookies we can't decrypt rather than fail the whole batch
			}
			value = dec
		}
		cookies = append(cookies, Cookie{
			Name: name, Value: value, Domain: hostKey, Path: path,
			Secure: isSecure != 0, HTTPOnly: isHTTPOnly != 0,
			Expires: chromeEpochToUnix(expiresUTC),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("no cookies found for %s yet — make sure you've logged in, then try confirm again", domain)
	}
	return cookies, nil
}

// DomainFromLoginURL extracts the registrable (apex) domain to filter
// cookies by from a platform's login URL (e.g. "https://www.producthunt.com/login" ->
// "producthunt.com", "https://gemini.google.com/app" -> "google.com").
//
// The apex domain, not the exact host, is used because auth session cookies
// (e.g. Google's SID/HSID/__Secure-*PSID) are set on the parent domain and
// shared across its subdomains — filtering by the exact login host would
// silently capture only unauthenticated analytics/product cookies scoped to
// that subdomain and miss the actual session.
func DomainFromLoginURL(loginURL string) (string, error) {
	u, err := url.Parse(loginURL)
	if err != nil {
		return "", err
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	if host == "" {
		return "", fmt.Errorf("no host in login URL %q", loginURL)
	}
	labels := strings.Split(host, ".")
	if len(labels) > 2 {
		host = strings.Join(labels[len(labels)-2:], ".")
	}
	return host, nil
}
