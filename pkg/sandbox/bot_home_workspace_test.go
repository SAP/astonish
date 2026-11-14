package sandbox

import "testing"

func TestBotTaskWorkspaceContract(t *testing.T) {
	valid := BotHomeWorkspacePublishRequest{PublicationID: "publication", TaskPath: "tasks/task", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("assignment")}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../TASK.md", "/TASK.md", "a/../TASK.md", "a\\b", ".", "a\x00b"} {
		q := valid
		q.Files = []BotHomeWorkspaceFile{{Path: p}}
		if q.Validate() == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	q := valid
	q.Files = append(q.Files, q.Files[0])
	if q.Validate() == nil {
		t.Fatal("duplicate")
	}
	q = valid
	q.Files = []BotHomeWorkspaceFile{{Path: "a"}, {Path: "a/b"}}
	if q.Validate() == nil {
		t.Fatal("ancestor alias")
	}
	q = valid
	q.Files = []BotHomeWorkspaceFile{{Path: "TASK.md", Data: make([]byte, MaxBotHomeWorkspaceAssetBytes+1)}}
	if q.Validate() == nil {
		t.Fatal("asset quota")
	}
	q = valid
	q.Files = make([]BotHomeWorkspaceFile, MaxBotHomeWorkspaceAssets+1)
	if q.Validate() == nil {
		t.Fatal("asset count")
	}
	if valid.Files[0].Digest() != "ce7a7c10b0dfd96808cca64c88cf5c5e13b7775283bdc924767887bfa32c8fa1" {
		t.Fatal("digest")
	}
}
