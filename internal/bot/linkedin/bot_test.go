package linkedin

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
	b := &LinkedInBot{}
	fn, ok := b.GetMethodByName("list_user_posts")
	if !ok {
		t.Fatal("expected list_user_posts method to be found")
	}

	// ListUserPosts validates profileURL before ever touching the page, so
	// an empty profileURL exercises the args[0] type assertion and returns
	// cleanly without needing a live browser page.
	var page browser.PageInterface = browser.NewRodPage(nil)
	_, err := fn(context.Background(), page, "", 0, "")
	if err == nil {
		t.Fatal("expected an error for empty profileURL")
	}
	if strings.Contains(err.Error(), "must be *rod.Page") {
		t.Fatalf("closure rejected a browser.PageInterface value: %v", err)
	}
	if !strings.Contains(err.Error(), "profileURL is required") {
		t.Fatalf("expected the profileURL validation error, got: %v", err)
	}
}
