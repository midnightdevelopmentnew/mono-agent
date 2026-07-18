package telegram

import "testing"

func TestTelegramResolveURL(t *testing.T) {
	b := &TelegramBot{}
	if got := b.ResolveURL("/foo"); got != "https://web.telegram.org/foo" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://t.me/foo"); got != "https://t.me/foo" {
		t.Errorf("ResolveURL absolute changed: %q", got)
	}
}

func TestTelegramExtractUsername(t *testing.T) {
	b := &TelegramBot{}
	cases := map[string]string{
		"@durov":                    "durov",
		"https://t.me/durov":        "durov",
		"https://telegram.me/durov": "durov",
		"https://t.me/joinchat/abc": "", // non-username path
		"https://t.me/":             "",
		"":                          "",
	}
	for in, want := range cases {
		if got := b.ExtractUsername(in); got != want {
			t.Errorf("ExtractUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTelegramPlatform(t *testing.T) {
	if (&TelegramBot{}).Platform() != "TELEGRAM" {
		t.Error("Platform() should be TELEGRAM")
	}
}
