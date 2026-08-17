package storage

import (
	"strings"
	"unicode"
)

var kanaRows = map[string][]string{
	"あ": {"あ", "い", "う", "え", "お", "ア", "イ", "ウ", "エ", "オ", "ぁ", "ぃ", "ぅ", "ぇ", "ぉ", "ァ", "ィ", "ゥ", "ェ", "ォ"},
	"か": {"か", "き", "く", "け", "こ", "が", "ぎ", "ぐ", "げ", "ご", "カ", "キ", "ク", "ケ", "コ", "ガ", "ギ", "グ", "ゲ", "ゴ"},
	"さ": {"さ", "し", "す", "せ", "そ", "ざ", "じ", "ず", "ぜ", "ぞ", "サ", "シ", "ス", "セ", "ソ", "ザ", "ジ", "ズ", "ゼ", "ゾ"},
	"た": {"た", "ち", "つ", "て", "と", "だ", "ぢ", "づ", "で", "ど", "タ", "チ", "ツ", "テ", "ト", "ダ", "ヂ", "ヅ", "デ", "ド"},
	"な": {"な", "に", "ぬ", "ね", "の", "ナ", "ニ", "ヌ", "ネ", "ノ"},
	"は": {"は", "ひ", "ふ", "へ", "ほ", "ば", "び", "ぶ", "べ", "ぼ", "ぱ", "ぴ", "ぷ", "ぺ", "ぽ", "ハ", "ヒ", "フ", "ヘ", "ホ", "バ", "ビ", "ブ", "ベ", "ボ", "パ", "ピ", "プ", "ペ", "ポ"},
	"ま": {"ま", "み", "む", "め", "も", "マ", "ミ", "ム", "メ", "モ"},
	"や": {"や", "ゆ", "よ", "ヤ", "ユ", "ヨ", "ゃ", "ゅ", "ょ", "ャ", "ュ", "ョ"},
	"ら": {"ら", "り", "る", "れ", "ろ", "ラ", "リ", "ル", "レ", "ロ"},
	"わ": {"わ", "を", "ん", "ワ", "ヲ", "ン", "ゐ", "ゑ", "ヰ", "ヱ"},
}

// SearchVariants returns distinct hiragana/katakana variants for LIKE queries.
func SearchVariants(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	values := []string{query, convertKana(query, true), convertKana(query, false)}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// IndexCondition builds a safe SQL condition for an A-Z, kana-row, or # index.
func IndexCondition(expression, index string) (string, []any) {
	index = strings.TrimSpace(index)
	if index == "" {
		return "", nil
	}
	if len([]rune(index)) == 1 {
		r := []rune(index)[0]
		if unicode.IsLetter(r) && r <= unicode.MaxASCII {
			letter := strings.ToUpper(index)
			return "UPPER(SUBSTR(" + expression + ",1,1))=?", []any{letter}
		}
	}
	if chars, ok := kanaRows[index]; ok {
		placeholders := make([]string, len(chars))
		args := make([]any, len(chars))
		for i, char := range chars {
			placeholders[i] = "?"
			args[i] = char
		}
		return "SUBSTR(" + expression + ",1,1) IN (" + strings.Join(placeholders, ",") + ")", args
	}
	if index == "#" {
		allKana := make([]string, 0, 120)
		for _, chars := range kanaRows {
			allKana = append(allKana, chars...)
		}
		placeholders := make([]string, len(allKana))
		args := make([]any, len(allKana))
		for i, char := range allKana {
			placeholders[i] = "?"
			args[i] = char
		}
		return "NOT (UPPER(SUBSTR(" + expression + ",1,1)) BETWEEN 'A' AND 'Z') AND SUBSTR(" + expression + ",1,1) NOT IN (" + strings.Join(placeholders, ",") + ")", args
	}
	return "1=0", nil
}

func convertKana(value string, toHiragana bool) string {
	return strings.Map(func(r rune) rune {
		if toHiragana && r >= 'ァ' && r <= 'ヶ' {
			return r - 0x60
		}
		if !toHiragana && r >= 'ぁ' && r <= 'ゖ' {
			return r + 0x60
		}
		return r
	}, value)
}
