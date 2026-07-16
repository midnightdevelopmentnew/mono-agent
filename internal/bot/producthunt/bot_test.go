package producthunt

import "testing"

func TestProductHuntResolveURL(t *testing.T) {
	b := &ProductHuntBot{}
	if got := b.ResolveURL("/posts/monomind"); got != "https://www.producthunt.com/posts/monomind" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://www.producthunt.com/posts/monomind"); got != "https://www.producthunt.com/posts/monomind" {
		t.Errorf("ResolveURL absolute changed unexpectedly: %q", got)
	}
}

func TestProductHuntPlatform(t *testing.T) {
	b := &ProductHuntBot{}
	if b.Platform() != "PRODUCTHUNT" {
		t.Errorf("Platform() = %q, want PRODUCTHUNT", b.Platform())
	}
}
