package launcher

import "encoding/json"

// Presentation only: the persisted row and all approval/transport bindings stay intact.
func botOwnerAnswerText(text string) string {
	var v struct {
		Type     string `json:"type"`
		Response struct {
			QuestionID string `json:"question_id"`
			Answer     string `json:"answer"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(text), &v) == nil && v.Type == "bot_owner_answer" && v.Response.QuestionID != "" && v.Response.Answer != "" {
		return "Answer: " + v.Response.Answer
	}
	return text
}
