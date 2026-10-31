package agent

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/redact"
)

// cloneAsk 返回题目的深拷贝（没有选项的题得到空切片，上报帧里是 []，不是 null）。
func cloneAsk(a *proto.Ask) *proto.Ask { return a.MapText(func(s string) string { return s }) }

// redactAsk 返回脱敏后的题目：agent 的问题里可能拼进凭据，上报 hub 与写审计都只用它。
func redactAsk(a *proto.Ask) *proto.Ask { return a.MapText(redact.Text) }

// distinctQuestions 报告各题题面是否互不相同。hub 与手机按（脱敏后的）题面作答，题面相同就分不清答的是哪道。
func distinctQuestions(a *proto.Ask) bool {
	seen := make(map[string]bool, len(a.Questions))
	for _, q := range a.Questions {
		if seen[q.Question] {
			return false
		}
		seen[q.Question] = true
	}
	return true
}

// checkAnswers 校验以题面为键的答案：a 的每道题都要有答案，非空且不超过 proto.AnswerMax 字符；多余的键不管。没有题目也算不合规。
// 口径比 hub 的 checkAnswers（internal/hub/requests.go）宽，是有意的：hub 是答案的入口，在那里把关（去掉首尾空白后非空、
// 不收多余的键），更严是安全的一边；这里只兜底保证交给钩子的每题都有答案、不超长，多余的键换回原题面时丢掉，不为此把答案退回终端。
func checkAnswers(a *proto.Ask, answers map[string]string) error {
	if a == nil || len(a.Questions) == 0 {
		return errors.New("没有题目")
	}
	for i, q := range a.Questions {
		ans, ok := answers[q.Question]
		switch {
		case !ok:
			return fmt.Errorf("第 %d 题没有答案", i+1)
		case ans == "":
			return fmt.Errorf("第 %d 题的答案为空", i+1)
		case utf8.RuneCountInString(ans) > proto.AnswerMax:
			return fmt.Errorf("第 %d 题的答案超过 %d 字符", i+1, proto.AnswerMax)
		}
	}
	return nil
}

// originalAnswers 把以脱敏题面（red）为键的答案按题目序号换成以原题面（orig）为键：hub 与手机只见到脱敏后的题面，
// 钩子回填时 Claude Code 要的是原题面。答案不合规（见 checkAnswers）时返回错误。red 由 orig 脱敏而来，两者题数相同。
func originalAnswers(orig, red *proto.Ask, answers map[string]string) (map[string]string, error) {
	if err := checkAnswers(red, answers); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(red.Questions))
	for i, q := range red.Questions {
		out[orig.Questions[i].Question] = answers[q.Question]
	}
	return out, nil
}

// auditAnswers 是审计里记的答案：键是脱敏后的题面，值也过 redact.Text（答案是用户自己打的字，也可能贴进凭据）；多余的键不记。
func auditAnswers(red *proto.Ask, answers map[string]string) map[string]string {
	out := make(map[string]string, len(red.Questions))
	for _, q := range red.Questions {
		out[q.Question] = redact.Text(answers[q.Question])
	}
	return out
}
