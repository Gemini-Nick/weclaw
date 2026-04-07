package messaging

import (
	"fmt"
	"strings"
)

type replyKind string

const (
	replyKindAgent     replyKind = "agent"
	replyKindStatus    replyKind = "status"
	replyKindHelp      replyKind = "help"
	replyKindSwitch    replyKind = "switch"
	replyKindUsage     replyKind = "usage"
	replyKindSave      replyKind = "save"
	replyKindError     replyKind = "error"
	replyKindLinkhoard replyKind = "linkhoard"
)

func normalizePersonaAgent(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	switch name {
	case "cc", "claude code":
		return "claude"
	case "cx", "openai":
		return "codex"
	default:
		return name
	}
}

func personaHeader(agentName string, kind replyKind) string {
	switch normalizePersonaAgent(agentName) {
	case "claude":
		return fmt.Sprintf("Claude｜%s", personaTitle("小克总", kind))
	case "codex":
		return fmt.Sprintf("OpenAI｜%s", personaTitle("小德总", kind))
	default:
		return ""
	}
}

func personaTitle(nickname string, kind replyKind) string {
	switch kind {
	case replyKindSave:
		return nickname + "回执"
	case replyKindError:
		return nickname + "异常提示"
	case replyKindHelp, replyKindUsage:
		return nickname + "指令说明"
	case replyKindStatus:
		return nickname + "状态播报"
	case replyKindSwitch:
		return nickname + "切换确认"
	case replyKindLinkhoard:
		return nickname + "收录播报"
	default:
		return nickname + "汇报"
	}
}

func formatBrandedReply(agentName, detail string, kind replyKind) string {
	detail = strings.TrimSpace(MarkdownToPlainText(detail))
	if detail == "" {
		return ""
	}

	header := personaHeader(agentName, kind)
	if header == "" {
		return detail
	}

	return header + "： " + detail
}
