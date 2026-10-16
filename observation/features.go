package observation

import (
	"strings"
	"unicode"
)

// The feature extractor below serves the knowledge exploration engine
// (docs/agent-knowledge-exploration.md §4.2). It deliberately shares nothing
// with hints.go's contentTokens: that matcher is tuned by the priming
// benchmark corpus (lexical-entity-topic.v3) and must stay byte-stable, while
// exploration needs richer normalization — identifier segmentation, Russian
// word-form convergence and significant short technical tokens.

// exploreStopwords carries the extended RU/EN noise list for exploration
// queries. It extends the priming stopword set with question scaffolding
// words that carry no matching signal ("how", "почему", "нужно"…).
var exploreStopwords = func() map[string]bool {
	const words = "a an and are as at be by for from in into is it of on or the to with this that these those was were will would should can could may might must if then than so no not do does did but its their our your my his her they them we you he she there here when where which who whom what how why also both each other more most some any all " +
		"как для или это эта эти тот та те что чтобы когда где кто чем потому свой своя свое есть был была было были будет будут быть надо нужно можно нельзя должен должна должно же бы ли если уже ещё еще только даже без более меньше между перед после над под про через себя собой весь вся всё все каждый любой такой такая такие другим другой другие " +
		"использовать используется используем требуется давай пусть вроде типа"
	result := make(map[string]bool)
	for _, word := range strings.Fields(words) {
		result[word] = true
		// Stopwords are checked after stemming, so their stemmed forms must be
		// present too («нужно» stems to «нужн» and would otherwise slip through).
		if strings.ContainsFunc(word, isCyrillicRune) {
			result[stemRussianWord(word)] = true
		}
	}
	return result
}()

// exploreSingleRuneTokens keeps one-rune tokens that name technologies and are
// meaningful for technical matching (C and R languages). Everything else must
// be at least two runes long.
var exploreSingleRuneTokens = map[string]bool{"c": true, "r": true}

// knowledgeFeatureTokens extracts the normalized lexical feature set of a
// text: case/punctuation-insensitive, identifier-aware (camelCase, snake_case,
// kebab-case, dotted paths split into words; letter↔digit runs stay glued so
// v2, k8s and jwt2024 survive) and Russian word-form aware so «миграция»,
// «миграции» and «миграцию» land on one token.
func knowledgeFeatureTokens(value string) map[string]bool {
	tokens := make(map[string]bool)
	for _, segment := range splitFeatureSegments(value) {
		token := normalizeFeatureWord(segment)
		if keepFeatureToken(token) {
			tokens[token] = true
		}
	}
	return tokens
}

// splitFeatureSegments cuts a raw string into word segments. Boundaries are
// any non-alphanumeric rune plus intra-word case transitions: lower→Upper
// (camelCase) and an Upper run followed by Upper+Lower (HTTPServer → HTTP,
// Server). Letters and digits never split from each other inside a run.
func splitFeatureSegments(value string) []string {
	var segments []string
	var builder strings.Builder
	prevLower, prevUpper, prevDigit := false, false, false
	upperRun := 0
	flush := func() {
		if builder.Len() > 0 {
			segments = append(segments, builder.String())
			builder.Reset()
		}
	}
	for _, r := range value {
		switch {
		case unicode.IsUpper(r):
			if prevLower || prevDigit {
				flush()
				upperRun = 1
			} else {
				upperRun++
			}
			builder.WriteRune(r)
			prevLower, prevUpper, prevDigit = false, true, false
		case unicode.IsLower(r):
			if prevUpper && upperRun > 1 {
				// HTTPServer: the last upper of the run belongs to the next
				// word. Flush everything before it and restart from it.
				word := builder.String()
				builder.Reset()
				builder.WriteString(word[len(word)-len(string(lastRune(word))):])
				flushTail := word[:len(word)-len(string(lastRune(word)))]
				if flushTail != "" {
					segments = append(segments, flushTail)
				}
				upperRun = 0
			}
			builder.WriteRune(r)
			prevLower, prevUpper, prevDigit = true, false, false
		case unicode.IsDigit(r):
			builder.WriteRune(r)
			prevLower, prevUpper, prevDigit = false, false, true
		default:
			flush()
			upperRun = 0
			prevLower, prevUpper, prevDigit = false, false, false
		}
	}
	flush()
	return segments
}

func lastRune(value string) rune {
	var last rune
	for _, r := range value {
		last = r
	}
	return last
}

// normalizeFeatureWord lowercases a single word segment, folds ё→е and applies
// light Russian suffix stripping so inflected forms of one lemma meet.
func normalizeFeatureWord(segment string) string {
	word := strings.ReplaceAll(strings.ToLower(segment), "ё", "е")
	if strings.ContainsFunc(word, isCyrillicRune) {
		word = stemRussianWord(word)
	}
	return word
}

func isCyrillicRune(r rune) bool {
	return r >= '\u0400' && r <= '\u04FF'
}

func keepFeatureToken(token string) bool {
	if token == "" {
		return false
	}
	if len([]rune(token)) == 1 {
		return exploreSingleRuneTokens[token]
	}
	return !exploreStopwords[token]
}

// russianWordSuffixes lists nominal/adjectival case endings longest-first.
// Stripping is iterative with a stem floor: the residue must keep at least
// four runes, which keeps short words intact while collapsing regular
// inflection («миграция/миграции/миграцию/миграциями» → «миграц»).
var russianWordSuffixes = []string{
	"иями",
	"ями", "ами", "иях", "его", "ему", "ими", "ыми", "ого", "ому", "ешь", "ете",
	"ия", "ии", "ие", "ию", "ей", "ой", "ый", "ая", "яя", "ое", "ее", "ые", "ах", "ях", "ам", "ям", "ем", "ом", "ов", "ев", "ий", "ую", "юю", "ья", "ье", "ью",
	"а", "я", "ы", "и", "е", "у", "ю", "о", "ь",
}

func stemRussianWord(word string) string {
	runes := []rune(word)
	for range 3 {
		if len(runes) <= 4 {
			break
		}
		stripped := false
		for _, suffix := range russianWordSuffixes {
			suffixRunes := []rune(suffix)
			if len(runes)-len(suffixRunes) < 4 {
				continue
			}
			if string(runes[len(runes)-len(suffixRunes):]) == suffix {
				runes = runes[:len(runes)-len(suffixRunes)]
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return string(runes)
}
