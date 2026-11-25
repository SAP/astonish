package launcher

import "testing"

func TestWithStudioChatInitializedBorrowsResult(t *testing.T) {
	var got *ChatFactoryResult
	server := &StudioServer{}
	WithStudioChatInitialized(func(result *ChatFactoryResult) { got = result })(server)
	result := &ChatFactoryResult{SharedResources: &ChatCatalogResources{}}
	server.chatInitialized(result)
	if got != result || got.SharedResources != result.SharedResources {
		t.Fatal("initialization observer replaced the Studio resource owner")
	}
	WithStudioChatInitialized(nil)(server)
	if server.chatInitialized != nil {
		t.Fatal("nil observer must remain optional")
	}
}
