package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

// English papers are authored article-first: the passage is written (or fetched
// from the web) before any question exists, and the item stage receives that
// passage as fixed input and may only insert numbered blanks or delete sentences
// from it. This module owns that two-phase flow.

// generateEnglishGroupWithArticle builds one section from a passage. When
// articleText is empty a new passage is acquired first.
func (g *Generator) generateEnglishGroupWithArticle(
	ctx context.Context,
	subject, groupID string,
	entries []models.SpecEntry,
	articleText string,
) ([]models.GeneratedQuestion, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("题组没有题位")
	}
	tag := englishSubjectTag(subject)
	kind := englishGroupKind(groupID)
	request := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		position, ok := models.EnglishQuestionPosition(entry.Points)
		if !ok {
			return nil, fmt.Errorf("spec %s 缺少有效英语题位", entry.ID)
		}
		request = append(request, map[string]any{
			"specId": entry.ID, "number": position, "score": entry.Score,
			"difficultyBand": entry.DiffBand,
		})
	}
	encoded, _ := json.Marshal(request)

	articleText = strings.TrimSpace(articleText)
	articleSpec := englishArticleRequestFor(subject, groupID, entries)
	if articleText != "" {
		// Repair path: the stored passage is reused so the paper keeps its
		// article and only the questions are rebuilt — but only when it really is
		// an article. A group that degraded to placeholders stores a "% TODO"
		// stand-in, which must not masquerade as the source text.
		articleText = stripBlankMarkers(articleText)
		if issue := validateArticle(articleText, articleSpec); issue != "" {
			fmt.Printf("[article] %s %s 已存材料不可用(%s)，重新获取文章\n",
				englishLabel(specSubjectOrTag(tag)), groupID, issue)
			articleText = ""
		} else {
			fmt.Printf("[article] %s %s 复用已存文章 %d 词\n",
				englishLabel(specSubjectOrTag(tag)), groupID, len(englishGroupWordPattern.FindAllString(articleText, -1)))
		}
	}
	if articleText == "" {
		acquired, err := g.acquireArticle(ctx, articleSpec)
		if err != nil {
			return nil, err
		}
		articleText = acquired.Text
		fmt.Printf("[article] %s %s 文章来源=%s 主题=%q %d 词\n",
			englishLabel(specSubjectOrTag(tag)), groupID, acquired.Source, acquired.Topic, len(englishGroupWordPattern.FindAllString(articleText, -1)))
	}
	if issue := validateArticle(articleText, articleSpec); issue != "" {
		return nil, fmt.Errorf("文章不可用: %s", issue)
	}

	groupType, groupRules := englishGroupInstructions(tag, kind, groupID)
	answerContract := englishGroupAnswerContract(kind)
	user := fmt.Sprintf(`下面是一篇已经定稿的英文文章（正文不得改写，只能按题目要求挖空或删除整句）：

<article>
%s
</article>

请基于这篇文章出题。
题组类型：%s
题组编号：%s
题位规格（JSON）：%s

命题规则：
%s
不得引入文章之外的背景、人物、数据或事实；所有题干与选项都必须能在文章中定位依据。文章主题：%s。

只返回 JSON 对象，结构严格为：{"sharedMaterial":"按要求处理后的完整文章正文","sharedOptions":[],"items":[{"specId":"题位 ID","stem":"该小题题干","options":["选项A","选项B","选项C","选项D"],"answer":"答案：X。中文解析……","difficulty":0.55}]}。
题位必须逐项对应，不得增加或遗漏。%s`,
		articleText, groupType, groupID, encoded, groupRules, articleTitle(articleText), answerContract)
	system := fmt.Sprintf("你是熟悉 2026 年全国硕士研究生招生考试英语（%s）结构的命题专家。你只依据给定的文章命题：不改写正文，不引入文章之外的信息，材料、题干、选项和答案必须自洽。", englishSubjectChineseNumber(tag))
	model := g.selectModel(models.TypeChoice)
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	var response english2GroupResponse
	err := g.LLM.ChatJSON(ctx, model, messages, &response)
	if err != nil && g.FallbackModel != "" && g.FallbackModel != model {
		err = g.LLM.ChatJSON(ctx, g.FallbackModel, messages, &response)
	}
	if err != nil {
		return nil, err
	}
	if issue := validateEnglishGroupResponse(tag, groupID, entries, response); issue != "" {
		lastIssue := issue
		for attempt := 1; attempt <= 2; attempt++ {
			messages[1].Content = user + fmt.Sprintf("\n\n上一响应未通过第 %d 次结构校验：%s。请完整重做，不要修补或省略字段。%s", attempt, lastIssue, englishGroupRetryHint(kind))
			var retry english2GroupResponse
			if retryErr := g.LLM.ChatJSON(ctx, model, messages, &retry); retryErr != nil {
				lastIssue = fmt.Sprintf("结构修复请求失败: %v", retryErr)
				continue
			}
			if retryIssue := validateEnglishGroupResponse(tag, groupID, entries, retry); retryIssue != "" {
				lastIssue = retryIssue
				continue
			}
			response = retry
			lastIssue = ""
			break
		}
		if lastIssue != "" {
			return nil, fmt.Errorf("题组结构不合格: %s", lastIssue)
		}
	}
	// The questions must be derived from the article: only numbered blanks may be
	// added and only whole sentences may be dropped. This is the invariant that
	// makes the paper article-first rather than topic-predicted.
	if drifted := articleDrift(articleText, response.SharedMaterial); len(drifted) > 0 {
		messages[1].Content = user + fmt.Sprintf("\n\n上一响应改写了文章正文，以下句子在原文中找不到：%s。请严格保留原文句子（完形填空只能把被考查的词替换成 [n] 编号空；Part B 只能删掉整句并在原位置留 [n]），重新返回完整 JSON。", strings.Join(drifted[:minInt(3, len(drifted))], " / "))
		var retry english2GroupResponse
		if retryErr := g.LLM.ChatJSON(ctx, model, messages, &retry); retryErr != nil {
			return nil, fmt.Errorf("文章一致性修复失败: %w", retryErr)
		}
		if issue := validateEnglishGroupResponse(tag, groupID, entries, retry); issue != "" {
			return nil, fmt.Errorf("文章一致性修复后结构不合格: %s", issue)
		}
		if drifted := articleDrift(articleText, retry.SharedMaterial); len(drifted) > 0 {
			return nil, fmt.Errorf("题组改写了文章正文：%s", strings.Join(drifted[:minInt(3, len(drifted))], " / "))
		}
		response = retry
	}

	buildGroup := func(groupResponse english2GroupResponse) ([]models.GeneratedQuestion, map[string]string) {
		items := make(map[string]english2GroupItem, len(groupResponse.Items))
		for _, item := range groupResponse.Items {
			items[item.SpecID] = item
		}
		questions := make([]models.GeneratedQuestion, 0, len(entries))
		weak := make(map[string]string)
		for _, entry := range entries {
			item := items[entry.ID]
			options := item.Options
			if kind == "part-b" {
				options = groupResponse.SharedOptions
			}
			question, finishErr := g.finishForSubject(subject, entry, generationResponse{
				Stem: item.Stem, Options: options, Answer: item.Answer, Difficulty: item.Difficulty,
				SharedMaterial: groupResponse.SharedMaterial, SharedOptions: groupResponse.SharedOptions,
			})
			if finishErr != nil {
				weak[entry.ID] = finishErr.Error()
				continue
			}
			question.GroupID = groupID
			if question.Status != "ready" {
				weak[entry.ID] = strings.Join(question.Warnings, "；")
			}
			questions = append(questions, question)
		}
		return questions, weak
	}
	questions, weak := buildGroup(response)
	if len(weak) == 0 {
		return questions, nil
	}

	// Structure can be valid while a few answers are too thin. Regenerating a
	// twenty-item group to fix one explanation mostly fails again (the model
	// rewrites the whole passage), so only the weak items are rewritten in
	// place: the shared passage stays fixed, but the weak items' stems, options
	// and answers may all be rewritten (a self-correction artifact can land in a
	// stem or option, not just the answer, so answer-only repair would loop
	// forever). Three rounds because a single bad item must not cost the whole
	// passage.
	for attempt := 1; attempt <= 3 && len(weak) > 0; attempt++ {
		weakList := englishWeakItemPrompt(entries, response, weak)
		messages[1].Content = user + fmt.Sprintf("\n\n题组结构与材料已通过校验，只有下列小题不合格（第 %d 次修复）。sharedMaterial 与 sharedOptions 必须保持不变；只重写这些不合格小题的 stem、options、answer（其余小题原样返回）。不得出现“题目有误/重新审视/修改题面/假设题意/如果题目要求/选项中没有/重新设计/重新核对”等自我纠错或不确定性措辞；题干须自洽且信息足以确定答案。\n%s\n修复要求：%s\n格式示例（务必照此结构写全四个选项）：答案：B。A项：与原文不符，……；B项：符合上下文，……；C项：……；D项：……。", attempt, weakList, englishGroupAnswerRequirement(kind))
		var rewrite english2GroupResponse
		if retryErr := g.LLM.ChatJSON(ctx, model, messages, &rewrite); retryErr != nil {
			break
		}
		mergeEnglishGroupItems(&response, rewrite)
		questions, weak = buildGroup(response)
	}
	if len(weak) > 0 {
		return nil, fmt.Errorf("题组质量修复后仍未通过: %v", weak)
	}
	return questions, nil
}

// articleTitle returns a short label for the passage (its first sentence's head),
// used only inside prompts.
func articleTitle(text string) string {
	first := strings.SplitN(strings.TrimSpace(text), ".", 2)[0]
	if len(first) > 90 {
		first = first[:90]
	}
	return strings.TrimSpace(first)
}

// articleDrift reports the material sentences that cannot be traced back to the
// article. The allowed edits are exactly two: replacing tested words with [n]
// markers (cloze, Part B, translation gaps) and deleting whole sentences
// (Part B), so the comparison is per sentence — a marker-bearing sentence must
// still contain most of the original wording in order, and a marker-free
// sentence must match the original verbatim.
func articleDrift(articleText, material string) []string {
	if strings.TrimSpace(material) == "" {
		return []string{"(空材料)"}
	}
	articleSentences := splitArticleSentences(collapseSpaces(articleText))
	materialSentences := splitArticleSentences(collapseSpaces(material))
	cursor, skipped, markers, drifted := 0, 0, 0, []string{}
	for _, sentence := range materialSentences {
		key := normalizeArticleCompare(sentence)
		runes := []rune(key)
		if len(runes) < 12 && !englishClozeMarkerPattern.MatchString(sentence) {
			continue
		}
		blanked := englishClozeMarkerPattern.MatchString(sentence)
		plain := normalizeArticleCompare(englishClozeMarkerPattern.ReplaceAllString(sentence, " "))
		if strings.TrimSpace(plain) == "" {
			// A sentence made only of markers (gaps appended at the end of the
			// passage) carries no wording to trace back.
			markers += len(englishClozeMarkerPattern.FindAllString(sentence, -1))
			continue
		}
		found := false
		for index := cursor; index < len(articleSentences); index++ {
			original := articleSentences[index]
			originalKey := normalizeArticleCompare(original)
			if blanked {
				if isTokenSubsequence(plain, originalKey) {
					markers += len(englishClozeMarkerPattern.FindAllString(sentence, -1))
					cursor, found = index+1, true
					break
				}
			} else if strings.Contains(originalKey, key) {
				cursor, found = index+1, true
				break
			}
			skipped++
		}
		if !found {
			drifted = append(drifted, ellipsize(strings.TrimSpace(sentence), 70))
		}
	}
	if len(drifted) == 0 && skipped > markers+2 {
		drifted = append(drifted, fmt.Sprintf("题组删除了 %d 句原文但只有 %d 个编号空", skipped, markers))
	}
	return drifted
}

// isTokenSubsequence reports whether every word of part appears in whole, in
// order. Blank insertion removes the tested words, so the surviving wording must
// still be a subsequence of the original sentence.
func isTokenSubsequence(part, whole string) bool {
	parts := strings.Fields(part)
	if len(parts) == 0 {
		return false
	}
	wholes := strings.Fields(whole)
	index := 0
	for _, word := range parts {
		for index < len(wholes) && wholes[index] != word {
			index++
		}
		if index == len(wholes) {
			return false
		}
		index++
	}
	// At least half of the original wording must survive, so dropping a sentence
	// down to a couple of words is not accepted as blank insertion.
	return len(parts)*2 >= len(wholes)
}

// normalizeArticleCompare lowercases and strips punctuation so sentence
// comparisons survive formatting-only differences.
func normalizeArticleCompare(text string) string {
	replacer := strings.NewReplacer(
		"\n", " ", "\u2018", "'", "\u2019", "'", "\u201c", "\"", "\u201d", "\"",
		"\u2014", "-", "\u2013", "-", "  ", " ",
	)
	normalized := strings.ToLower(replacer.Replace(text))
	var b strings.Builder
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '\'':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return collapseSpaces(b.String())
}

// splitArticleSentences splits prose into sentences for comparison.
func splitArticleSentences(text string) []string {
	var sentences []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '?' || r == '!' {
			sentences = append(sentences, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if remainder := strings.TrimSpace(current.String()); remainder != "" {
		sentences = append(sentences, remainder)
	}
	return sentences
}

// ellipsize shortens prose for prompts and error messages.
func ellipsize(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// specSubjectOrTag maps a knowledge-point tag back to a display subject.
func specSubjectOrTag(tag string) string {
	if tag == "english1" {
		return "英语一"
	}
	return "英语二"
}

// sortEntriesByPosition orders entries by their English position (helper for
// deterministic article planning and tests).
func sortEntriesByPosition(entries []models.SpecEntry) []models.SpecEntry {
	ordered := append([]models.SpecEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, _ := models.EnglishQuestionPosition(ordered[i].Points)
		right, _ := models.EnglishQuestionPosition(ordered[j].Points)
		return left < right
	})
	return ordered
}
