package action

import "testing"

// TestResolvePathCountAndLength is a regression test: "{{variable.count}}" and
// "{{variable.length}}" templates previously only worked when the variable
// came from a tracked step result (resolveStepResultField's special-cased
// "count" branch). For any variable set via plain SetVariable (e.g. a
// call_bot_method result stored under variable_name, not a step id), these
// templates silently resolved to nil/empty — affecting several action JSONs
// (instagram/linkedin's list_post_comments.count, tiktok's list_video_comments
// .length, etc.) and the linkedin/x publish_post "media.length > 0" condition,
// which was permanently false because "media" is a plain string variable.
func TestResolvePathCountAndLength(t *testing.T) {
	ctx := NewExecutionContext()
	ctx.SetVariable("comments", []interface{}{"a", "b", "c"})
	ctx.SetVariable("media", "/tmp/photo.jpg")
	ctx.SetVariable("emptyMedia", "")

	vr := NewVariableResolver(ctx)

	if got := vr.ResolvePath("comments.count"); got != 3 {
		t.Errorf("comments.count = %v, want 3", got)
	}
	if got := vr.ResolvePath("media.length"); got != len("/tmp/photo.jpg") {
		t.Errorf("media.length = %v, want %d", got, len("/tmp/photo.jpg"))
	}
	if got := vr.ResolvePath("emptyMedia.length"); got != 0 {
		t.Errorf("emptyMedia.length = %v, want 0", got)
	}
}
