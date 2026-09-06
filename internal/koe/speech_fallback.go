package koe

import "unicode"

// Mechanical spoken fallbacks — the lines Koe speaks WITHOUT the back-brain when a
// delegation fails, is rejected, is misheard, or needs an agent clarification.
// exactSpeechInstructions reads them verbatim (they bypass the Realtime model), so a
// hardcoded language would speak Chinese to an English user and vice versa (the S7
// bug). The table is keyed by language, then by a stable message id; every id MUST
// exist under every language so fallbackSay never returns "".
var fallbackSpeech = map[string]map[string]string{
	"zh": {
		"transport_failed":   "抱歉，刚才没能完成，连接出了点问题。",
		"busy":               "现在有点忙，稍等一下再说一次好吗？",
		"misheard":           "我没听清，能再说一次吗？",
		"clarify_which":      "你是指哪个 agent？",
		"clarify_unknown":    "我没找到这个 agent，你是指哪一个？",
		"incomplete":         "这个刚才没能做完，你要我再试一次吗？",
		"clarify_which_task": "现在有几件事在跑，你是指哪一个？",
	},
	"en": {
		"transport_failed":   "Sorry, I couldn't finish that — there was a connection problem.",
		"busy":               "I'm a bit busy right now — could you say that again in a moment?",
		"misheard":           "I didn't catch that — could you say it again?",
		"clarify_which":      "Which agent do you mean?",
		"clarify_unknown":    "I couldn't find that agent — which one do you mean?",
		"incomplete":         "I didn't manage to finish that — want me to try again?",
		"clarify_which_task": "A few tasks are running — which one do you mean?",
	},
}

// fallbackLangDefault is the language used when neither a pinned koe language nor a
// Han-script utterance signal is present (a silent/garbled/Latin utterance with no
// pin). English is the safer neutral here: the reported S7 bug was English users
// hearing Chinese, and an empty koe.language means "follow the utterance", where the
// absence of any Han rune is itself the signal that the user is not speaking Chinese.
const fallbackLangDefault = "en"

// fallbackSay returns the mechanical fallback line for (lang, key), degrading to the
// default language so a missing per-language entry never speaks an empty line.
func fallbackSay(lang, key string) string {
	if m, ok := fallbackSpeech[lang]; ok {
		if s := m[key]; s != "" {
			return s
		}
	}
	return fallbackSpeech[fallbackLangDefault][key]
}

// fallbackLang picks the language for a mechanical fallback line. A pinned koe
// language (Settings → Voice → Language) wins when it maps to a table we ship
// (en/zh); otherwise the user's own utterance decides — any Han (CJK ideograph)
// rune → zh, else the default (en). A "ja"/other pin has no table yet and falls
// through to utterance inference; task scope is zh+en, so a dedicated non-en/zh
// fallback table is future work (a Japanese utterance's kanji reads as Han → zh,
// an accepted limitation until a ja table lands).
func fallbackLang(pinned, utterance string) string {
	switch pinned {
	case "en":
		return "en"
	case "zh":
		return "zh"
	}
	if containsHan(utterance) {
		return "zh"
	}
	return fallbackLangDefault
}

// containsHan reports whether s carries a Han (CJK ideograph) rune — the
// dependency-free signal that the user's utterance was Chinese (or CJK).
func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// joinHuman renders candidate slugs into a spoken "A 还是 B" / "A or B" choice in the
// given language.
func joinHuman(lang string, slugs []string) string {
	conj := " or "
	if lang == "zh" {
		conj = " 还是 "
	}
	switch len(slugs) {
	case 0:
		return ""
	case 1:
		return slugs[0]
	default:
		out := slugs[0]
		for _, s := range slugs[1:] {
			out += conj + s
		}
		return out
	}
}

// clarifyWhich builds the ambiguous-agent prompt: the "which agent" question plus
// the candidate list. Chinese concatenates without a separating space (preserving
// the original verbatim contract); other languages insert a space.
func clarifyWhich(lang string, candidates []string) string {
	q := fallbackSay(lang, "clarify_which")
	list := joinHuman(lang, candidates)
	if list == "" {
		return q
	}
	if lang == "zh" {
		return q + list
	}
	return q + " " + list
}

func clarifyWhichTask(lang string, tasks []VoiceTask) string {
	labels := make([]string, len(tasks))
	for i, task := range tasks {
		labels[i] = task.Label
	}
	question := fallbackSay(lang, "clarify_which_task")
	list := joinHuman(lang, labels)
	if list == "" {
		return question
	}
	if lang == "zh" {
		return question + list
	}
	return question + " " + list
}
