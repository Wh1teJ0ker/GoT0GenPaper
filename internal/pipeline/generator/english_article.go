package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

// English papers are written article-first: the passage is produced (or fetched
// from the web) before any question exists, and every item is then derived from
// that passage. This file owns article acquisition; question authoring stays in
// generator.go's English group flow.

// ArticleSourceKind selects where an English passage comes from.
type ArticleSourceKind string

const (
	// ArticleSourceWrite has the model write an original article (default: it
	// needs no network and no API key beyond the configured provider).
	ArticleSourceWrite ArticleSourceKind = "write"
	// ArticleSourceWebSearch searches the web for a real article and uses its
	// text as the passage, falling back to ArticleSourceWrite when the search or
	// fetch fails.
	ArticleSourceWebSearch ArticleSourceKind = "websearch"
)

// ArticleSourceOptions configures web search. A search endpoint is required for
// ArticleSourceWebSearch; without it the generator silently uses written
// articles so a paper can still be produced offline.
type ArticleSourceOptions struct {
	Kind        ArticleSourceKind
	SearchURL   string // template containing {query}, e.g. https://api.search.brave.com/res/v1/web/search?q={query}
	APIKey      string
	MaxResults  int
	HTTPTimeout time.Duration
}

// article is one passage with the topic it was written or fetched for.
type article struct {
	Topic string `json:"topic"`
	Text  string `json:"article"`
	// Source records "write" or "websearch:<host>" for the manifest/README.
	Source string `json:"-"`
}

// englishArticleRequest describes the passage a section needs.
type englishArticleRequest struct {
	section   string // 完形填空 / 阅读理解 Part A / Part B / 翻译
	subject   string // 英语一 / 英语二
	tag       string // english1 / english2
	groupID   string
	textIndex int // Part A text number, 0 otherwise
	minWords  int
	maxWords  int
	hasBlanks bool // the item stage inserts numbered blanks
	removeGap bool // the item stage deletes sentences to create Part B gaps
}

func englishArticleRequestFor(subject, groupID string, entries []models.SpecEntry) englishArticleRequest {
	tag := englishSubjectTag(subject)
	kind := englishGroupKind(groupID)
	request := englishArticleRequest{subject: englishLabelFor(tag), tag: tag, groupID: groupID}
	switch kind {
	case "cloze":
		request.section = "完形填空（英语知识运用）"
		request.minWords, request.maxWords = 250, 360
		request.hasBlanks = true
	case "part-b":
		request.section = "阅读理解 Part B（新题型）"
		request.minWords, request.maxWords = 250, 360
		request.removeGap = true
	case "translation":
		request.section = "翻译 Part C（英译汉）"
		request.minWords, request.maxWords = 150, 230
	default:
		request.section = "阅读理解 Part A"
		request.minWords, request.maxWords = 280, 420
		if len(entries) > 0 {
			if position, ok := models.EnglishQuestionPosition(entries[0].Points); ok {
				request.textIndex = (position-21)/5 + 1
			}
		}
	}
	return request
}

func englishLabelFor(tag string) string {
	if tag == "english1" {
		return "英语一"
	}
	return "英语二"
}

// acquireArticle produces the passage for one section: it fetches a real article
// when web search is configured, and otherwise (or on any failure) asks the model
// to write one. The returned error is only non-nil when no article could be
// produced at all.
func (g *Generator) acquireArticle(ctx context.Context, request englishArticleRequest) (article, error) {
	if options := g.ArticleSource; options.Kind == ArticleSourceWebSearch && strings.TrimSpace(options.SearchURL) != "" {
		fetched, err := g.fetchArticle(ctx, request, options)
		if err == nil {
			if conflict := g.articleConflict(fetched); conflict != "" {
				fmt.Printf("[article] 搜索结果与已用材料重复(%s)，改用撰写模式\n", conflict)
			} else {
				g.rememberArticle(fetched)
				return fetched, nil
			}
		}
		// A failed search must not lose the paper: write one instead and keep the
		// reason on the article for the caller to surface.
		written, writeErr := g.writeArticle(ctx, request)
		if writeErr != nil {
			return article{}, fmt.Errorf("websearch: %v; write fallback: %w", err, writeErr)
		}
		written.Source = "write (websearch failed: " + err.Error() + ")"
		g.rememberArticle(written)
		return written, nil
	}
	written, err := g.writeArticle(ctx, request)
	if err != nil {
		return article{}, err
	}
	g.rememberArticle(written)
	return written, nil
}

// ResetArticleHistory starts a new paper: sections must not repeat each other's
// passages, but the next paper is free to reuse topics.
func (g *Generator) ResetArticleHistory() {
	g.articleHistory = nil
}

// rememberArticle records a passage so later sections avoid its topic.
func (g *Generator) rememberArticle(article article) {
	g.articleHistory = append(g.articleHistory, articleHistoryEntry{
		topic:   strings.TrimSpace(article.Topic),
		opening: articleOpening(article.Text),
	})
}

// articleConflict reports why a candidate passage is too close to one already
// used in this paper (empty string means it is fine).
func (g *Generator) articleConflict(candidate article) string {
	opening := articleOpening(candidate.Text)
	topic := normalizeArticleCompare(candidate.Topic)
	for _, used := range g.articleHistory {
		if used.opening != "" && opening != "" && openingTokensShared(used.opening, opening) >= 6 {
			return "开头与已用文章相同"
		}
		if topic != "" && used.topic != "" && normalizeArticleCompare(used.topic) == topic {
			return "主题与已用文章相同"
		}
		if used.topic != "" && topic != "" && strings.Contains(normalizeArticleCompare(used.topic), topic) && len(topic) > 12 {
			return "主题与已用文章相近"
		}
	}
	return ""
}

// usedTopicList renders the topics already used, for the prompt.
func (g *Generator) usedTopicList() string {
	if len(g.articleHistory) == 0 {
		return ""
	}
	topics := make([]string, 0, len(g.articleHistory))
	for _, used := range g.articleHistory {
		if used.topic != "" {
			topics = append(topics, used.topic)
		}
	}
	return strings.Join(topics, "；")
}

// articleOpening normalizes the first twelve words of a passage.
func articleOpening(text string) string {
	words := strings.Fields(normalizeArticleCompare(text))
	if len(words) > 12 {
		words = words[:12]
	}
	return strings.Join(words, " ")
}

// openingTokensShared counts how many of the first tokens two openings share.
func openingTokensShared(left, right string) int {
	leftTokens := strings.Fields(left)
	rightTokens := strings.Fields(right)
	shared := 0
	for index := 0; index < len(leftTokens) && index < len(rightTokens); index++ {
		if leftTokens[index] != rightTokens[index] {
			break
		}
		shared++
	}
	return shared
}

// writeArticle asks the model for one original passage. Asking for the article
// alone (no questions yet) keeps the writing focused; the questions are authored
// in a separate call that receives this text as fixed input.
func (g *Generator) writeArticle(ctx context.Context, request englishArticleRequest) (article, error) {
	if g.LLM == nil {
		return article{}, fmt.Errorf("no model client configured")
	}
	topicHint := ""
	if request.textIndex > 0 {
		topicHint = fmt.Sprintf("这是 Part A 第 %d 篇阅读材料，请与同卷其他篇目的题材、体裁有所区别。", request.textIndex)
	}
	styleHint := "文章应面向考研英语考生，题材与往年真题风格相近（科普、社会、经济、文化、教育、环境等），观点清晰、有论证层次。"
	switch englishGroupKindOfGroup(request.groupID) {
	case "cloze":
		styleHint = "文章应是说明性或议论性的连贯短文，用词地道、句间逻辑清晰，适合挖 20 个词/短语空。"
	case "part-b":
		if request.tag == "english1" {
			styleHint = "文章应结构清晰、段间衔接自然，其中要有若干完整句子可以整体抽掉，作为七选五（句子填空）的空位。"
		} else {
			styleHint = "文章必须介绍或引用 5 位不同的具名人物（写出完整英文姓名），并清楚呈现每个人各自的观点或主张，使读者能据此把人物与观点一一对应。"
		}
	case "translation":
		styleHint = "文章应有几个结构完整、含长难句或指代关系的句子，适合作为英译汉考查点。"
	}
	system := fmt.Sprintf("你是考研英语（%s）命题组的英文作者，负责撰写试卷用的原创英文文章。", strings.TrimPrefix(request.subject, "英语"))
	exclusion := ""
	if used := g.usedTopicList(); used != "" {
		exclusion = fmt.Sprintf("\n本卷已经用过以下主题，请换一个完全不同的题材与开头（不要复用这些主题、人物或开头场景）：%s。", used)
	}
	user := fmt.Sprintf(`请为考研%s的「%s」部分写一篇原创英文文章。
主题：自选一个具体、有信息量的题目（不要泛泛而谈，例如不要只写 "technology" 而应写 "why repair cafés are spreading in European cities"）。
长度：%d-%d 个英文单词。
要求：%s
%s
只返回 JSON 对象，结构严格为 {"topic":"文章主题（英文短语）","article":"完整英文文章正文"}。不要在 article 里写题目、选项、中文、编号空或任何标记。%s`,
		request.subject, request.section, request.minWords, request.maxWords, styleHint, topicHint, exclusion)
	model := g.selectModel(models.TypeChoice)
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	var response article
	if err := g.LLM.ChatJSON(ctx, model, messages, &response); err != nil {
		if g.FallbackModel != "" && g.FallbackModel != model {
			if fallbackErr := g.LLM.ChatJSON(ctx, g.FallbackModel, messages, &response); fallbackErr != nil {
				return article{}, err
			}
		} else {
			return article{}, err
		}
	}
	response.Text = cleanArticleText(response.Text)
	if issue := g.articleIssue(response, request); issue != "" {
		// Up to two retries with the exact complaint: the article is the paper's
		// backbone, so it must be usable before any question is written.
		for attempt := 1; attempt <= 2 && g.articleIssue(response, request) != ""; attempt++ {
			lastIssue := g.articleIssue(response, request)
			messages[1].Content = user + "\n\n上一篇文章不合格（" + lastIssue + "）。请重新写一篇完整的文章：换一个题材、换一个开头场景，不要截断，也不要加入题目、编号空或中文。"
			var retry article
			if err := g.LLM.ChatJSON(ctx, model, messages, &retry); err != nil {
				return article{}, fmt.Errorf("文章不合格(%s)，重写失败: %w", lastIssue, err)
			}
			retry.Text = cleanArticleText(retry.Text)
			response = retry
		}
		if issue := g.articleIssue(response, request); issue != "" {
			return article{}, fmt.Errorf("文章不合格: %s", issue)
		}
	}
	response.Source = string(ArticleSourceWrite)
	return response, nil
}

// validateArticle checks that a passage is complete enough to carry a section.
func validateArticle(text string, request englishArticleRequest) string {
	if strings.TrimSpace(text) == "" {
		return "文章为空"
	}
	words := len(englishGroupWordPattern.FindAllString(text, -1))
	if words < request.minWords-40 {
		return fmt.Sprintf("文章仅 %d 个英文词，要求 %d-%d 词", words, request.minWords, request.maxWords)
	}
	if strings.Contains(text, "[") || strings.Contains(text, "答案") {
		return "文章里出现了编号空、题目或中文内容"
	}
	return ""
}

// articleIssue validates a passage and checks it against the passages already
// used in this paper (nil means the article is usable).
func (g *Generator) articleIssue(candidate article, request englishArticleRequest) string {
	if issue := validateArticle(candidate.Text, request); issue != "" {
		return issue
	}
	return g.articleConflict(candidate)
}

// cleanArticleText strips markdown decoration and collapses whitespace so later
// comparisons against the items are stable.
func cleanArticleText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\t", " ")
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "#")
		kept = append(kept, strings.TrimSpace(trimmed))
	}
	return strings.Join(kept, "\n\n")
}

// fetchArticle searches the web and returns the top usable result's text.
func (g *Generator) fetchArticle(ctx context.Context, request englishArticleRequest, options ArticleSourceOptions) (article, error) {
	query := englishSearchQuery(request)
	endpoint := strings.ReplaceAll(options.SearchURL, "{query}", url.QueryEscape(query))
	timeout := options.HTTPTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return article{}, fmt.Errorf("构造搜索请求: %w", err)
	}
	if options.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+options.APIKey)
		req.Header.Set("X-Subscription-Token", options.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return article{}, fmt.Errorf("搜索请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return article{}, fmt.Errorf("读取搜索结果: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return article{}, fmt.Errorf("搜索接口返回 %d", resp.StatusCode)
	}
	links := extractArticleLinks(body, options.MaxResults)
	for _, link := range links {
		text, fetchErr := fetchArticleText(ctx, client, link)
		if fetchErr != nil {
			continue
		}
		text = cleanArticleText(text)
		if issue := validateArticle(text, request); issue != "" {
			continue
		}
		return article{Topic: link, Text: text, Source: "websearch:" + hostOf(link)}, nil
	}
	return article{}, fmt.Errorf("搜索结果中没有可用的完整文章")
}

// englishSearchQuery turns a section request into a search query.
func englishSearchQuery(request englishArticleRequest) string {
	switch englishGroupKindOfGroup(request.groupID) {
	case "cloze":
		return "graduate entrance exam reading passage science society feature article"
	case "part-b":
		return "english feature article clear paragraphs structure opinion"
	case "translation":
		return "english essay long sentences science culture 200 words"
	default:
		return fmt.Sprintf("english magazine article society science culture (text %d)", request.textIndex)
	}
}

var (
	articleLinkPattern = regexp.MustCompile(`https?://[^\s"'<>\\]+`)
	htmlTagPattern     = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
)

// extractArticleLinks pulls candidate article URLs out of a search response. It
// accepts both JSON APIs (any "url" field) and plain HTML result pages.
func extractArticleLinks(body []byte, max int) []string {
	if max <= 0 {
		max = 3
	}
	seen := make(map[string]bool)
	var links []string
	add := func(link string) {
		link = strings.TrimSpace(strings.TrimRight(link, ".,);"))
		if link == "" || seen[link] || strings.HasSuffix(link, ".pdf") {
			return
		}
		seen[link] = true
		links = append(links, link)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		var walk func(value any)
		walk = func(value any) {
			switch typed := value.(type) {
			case map[string]any:
				for key, child := range typed {
					if strings.EqualFold(key, "url") {
						if text, ok := child.(string); ok {
							add(text)
						}
						continue
					}
					walk(child)
				}
			case []any:
				for _, child := range typed {
					walk(child)
				}
			}
		}
		walk(payload)
	}
	if len(links) == 0 {
		for _, match := range articleLinkPattern.FindAllString(string(body), -1) {
			add(html.UnescapeString(match))
			if len(links) >= max*3 {
				break
			}
		}
	}
	if len(links) > max {
		links = links[:max]
	}
	return links
}

// fetchArticleText downloads one page and reduces it to readable prose.
func fetchArticleText(ctx context.Context, client *http.Client, link string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoT0GenPaper/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("页面返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	text := htmlTagPattern.ReplaceAllString(string(body), " ")
	text = html.UnescapeString(text)
	if strings.Contains(text, "context") {
		// Search-engine boilerplate is not an article.
	}
	return collapseSpaces(text), nil
}

func collapseSpaces(text string) string {
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func hostOf(link string) string {
	if parsed, err := url.Parse(link); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "web"
}

// englishGroupKindOfGroup resolves a group id's section kind.
func englishGroupKindOfGroup(groupID string) string {
	return englishGroupKind(groupID)
}

// articleForGroup returns the passage already stored on a group's questions, so
// a repair can rebuild the items without rewriting the passage.
func articleForGroup(questions []models.GeneratedQuestion) string {
	for _, question := range questions {
		if text := strings.TrimSpace(question.SharedMaterial); text != "" {
			return text
		}
	}
	return ""
}

// stripBlankMarkers removes the numbered gaps an item stage inserted, so a
// stored cloze/Part B passage can be reused as the source article.
func stripBlankMarkers(material string) string {
	cleaned := englishClozeMarkerPattern.ReplaceAllString(material, " ")
	return collapseSpaces(cleaned)
}
