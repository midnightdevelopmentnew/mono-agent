package instagram

import (
	"context"
	"strings"
	"testing"

	"monoagent/internal/browser"
)

// TestGetMethodByNameAcceptsPageInterface is a regression test: the executor
// (internal/action/steps.go) prepends ae.page — declared type
// browser.PageInterface, concretely *browser.RodPage or *browser.ExtensionPage
// — as args[0] to every call_bot_method dispatch. GetMethodByName's returned
// closures previously asserted args[0].(*rod.Page), a concrete type that is
// never the runtime value, so every call_bot_method step against this bot
// failed with "first arg must be *rod.Page" instead of reaching the actual
// action method.
func TestGetMethodByNameAcceptsPageInterface(t *testing.T) {
	b := &InstagramBot{}
	fn, ok := b.GetMethodByName("get_user_info")
	if !ok {
		t.Fatal("expected get_user_info method to be found")
	}

	// The wrapper derives a username from its second arg and errors out
	// before ever touching the page if it can't determine one, so an empty
	// usernameOrURL exercises the args[0] type assertion and returns
	// cleanly without needing a live browser page.
	var page browser.PageInterface = browser.NewRodPage(nil)
	_, err := fn(context.Background(), page, "")
	if err == nil {
		t.Fatal("expected an error for an empty usernameOrURL")
	}
	if strings.Contains(err.Error(), "must be *rod.Page") {
		t.Fatalf("closure rejected a browser.PageInterface value: %v", err)
	}
	if !strings.Contains(err.Error(), "could not determine username") {
		t.Fatalf("expected the username-derivation error, got: %v", err)
	}
}
