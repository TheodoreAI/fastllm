package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"fastllm/internal/llm"
	xhtml "golang.org/x/net/html"
)

// DefaultMaxSearchResults is the default number of search results returned.
const DefaultMaxSearchResults = 5

// MaxSearchResultsLimit is the hard ceiling for requested search results.
const MaxSearchResultsLimit = 10

// DefaultMaxFetchBytes is the default byte cap for fetched web pages.
const DefaultMaxFetchBytes = 16384

// MaxFetchBytesLimit is the maximum allowed byte cap for fetched web pages.
const MaxFetchBytesLimit = 65536

// UserAgent used for web searches and fetching.
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 FastLLM/1.0"

// SearchTool defines the LLM tool definition for web search.
var SearchTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "web_search",
		Description: "Search the public web for real-time information, documentation, code examples, or news using a search query. Returns top matching results with titles, URLs, and snippets.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query (e.g. \"golang context timeout best practices\" or \"go 1.25 release notes\").",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of search results to return (default 5, max 10).",
				},
			},
			"required": []string{"query"},
		},
	},
}

// FetchTool defines the LLM tool definition for fetching web page contents.
var FetchTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "web_fetch",
		Description: "Fetch and read the text content of a web page at a given HTTP or HTTPS URL. Converts HTML to clean readable Markdown text, stripping boilerplate, scripts, and navigation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The HTTP or HTTPS URL to fetch (e.g. \"https://go.dev/blog/go1.25\").",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Maximum characters of text to return (default 16000, max 64000).",
				},
			},
			"required": []string{"url"},
		},
	},
}

// SearchResult represents a single web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Search performs a web search using DuckDuckGo HTML (or Brave Search API if BRAVE_SEARCH_API_KEY is present).
func Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query cannot be empty")
	}

	if maxResults <= 0 {
		maxResults = DefaultMaxSearchResults
	}
	if maxResults > MaxSearchResultsLimit {
		maxResults = MaxSearchResultsLimit
	}

	// 1. Try Brave Search API if key is provided
	if apiKey := strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY")); apiKey != "" {
		results, err := searchBrave(ctx, apiKey, query, maxResults)
		if err == nil && len(results) > 0 {
			return results, nil
		}
	}

	// 2. DuckDuckGo HTML search (no API key required)
	return searchDuckDuckGo(ctx, query, maxResults)
}

// SearchFormatted executes a search and formats the results as a Markdown string.
func SearchFormatted(ctx context.Context, query string, maxResults int) string {
	results, err := Search(ctx, query, maxResults)
	if err != nil {
		return fmt.Sprintf("Error performing web search: %v", err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("No search results found for %q.", query)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for %q:\n\n", query))
	for i, res := range results {
		title := res.Title
		if title == "" {
			title = res.URL
		}
		sb.WriteString(fmt.Sprintf("%d. [%s](%s)\n", i+1, title, res.URL))
		if res.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", res.Snippet))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	formData := url.Values{}
	formData.Set("q", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create ddg request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ddg search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ddg search returned status %d", resp.StatusCode)
	}

	doc, err := xhtml.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse ddg html: %w", err)
	}

	var results []SearchResult
	var traverse func(*xhtml.Node)

	traverse = func(n *xhtml.Node) {
		if len(results) >= maxResults {
			return
		}

		if n.Type == xhtml.ElementNode && n.Data == "div" {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "result") && !strings.Contains(a.Val, "results") {
					res := parseDDGResultNode(n)
					if res != nil && res.URL != "" {
						results = append(results, *res)
						if len(results) >= maxResults {
							return
						}
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}

	traverse(doc)
	return results, nil
}

func parseDDGResultNode(n *xhtml.Node) *SearchResult {
	var title, href, snippet string

	var walk func(*xhtml.Node)
	walk = func(child *xhtml.Node) {
		if child.Type == xhtml.ElementNode {
			if child.Data == "a" {
				for _, attr := range child.Attr {
					if attr.Key == "class" && strings.Contains(attr.Val, "result__snippet") {
						snippet = extractText(child)
					}
					if attr.Key == "class" && strings.Contains(attr.Val, "result__a") {
						title = extractText(child)
						for _, a := range child.Attr {
							if a.Key == "href" {
								href = cleanDDGURL(a.Val)
							}
						}
					}
				}
			}
		}
		for c := child.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(n)

	if href == "" || (title == "" && snippet == "") {
		return nil
	}

	return &SearchResult{
		Title:   strings.TrimSpace(title),
		URL:     href,
		Snippet: strings.TrimSpace(snippet),
	}
}

func cleanDDGURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	// Handle DuckDuckGo redirect URLs: /l/?uddg=https%3A%2F%2F...
	if strings.Contains(raw, "duckduckgo.com/l/?") || strings.HasPrefix(raw, "/l/?") {
		if parsed, err := url.Parse(raw); err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	return raw
}

func searchBrave(ctx context.Context, apiKey, query string, maxResults int) ([]SearchResult, error) {
	reqURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), maxResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)
	req.Header.Set("User-Agent", UserAgent)

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave search api returned status %d", resp.StatusCode)
	}

	var data struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []SearchResult
	for _, item := range data.Web.Results {
		if len(results) >= maxResults {
			break
		}
		results = append(results, SearchResult{
			Title:   item.Title,
			URL:     item.URL,
			Snippet: item.Description,
		})
	}
	return results, nil
}

// Fetch retrieves the web page content at targetURL and returns clean Markdown text.
func Fetch(ctx context.Context, targetURL string, maxBytes int) (string, error) {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return "", errors.New("url cannot be empty")
	}

	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid URL %q: must be http or https", targetURL)
	}

	if maxBytes <= 0 {
		maxBytes = DefaultMaxFetchBytes
	}
	if maxBytes > MaxFetchBytesLimit {
		maxBytes = MaxFetchBytesLimit
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("create fetch request: %w", err)
	}

	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := publicHTTPClient(20 * time.Second)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("server returned HTTP %d %s", resp.StatusCode, resp.Status)
	}

	// Read up to 4x maxBytes to allow stripping HTML without running out of content
	limitReader := io.LimitReader(resp.Body, int64(maxBytes*4))
	rawBytes, err := io.ReadAll(limitReader)
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	var output string

	if strings.Contains(contentType, "application/json") {
		var pretty bytes.Buffer
		if json.Indent(&pretty, rawBytes, "", "  ") == nil {
			output = pretty.String()
		} else {
			output = string(rawBytes)
		}
	} else if strings.Contains(contentType, "text/plain") {
		output = string(rawBytes)
	} else {
		// Default to HTML parsing
		output = HTMLToMarkdown(string(rawBytes), targetURL)
	}

	truncated := false
	if len(output) > maxBytes {
		output = output[:maxBytes]
		truncated = true
	}

	if truncated {
		output += fmt.Sprintf("\n\n[Content truncated — retrieved first %d characters]", maxBytes)
	}

	return output, nil
}

// publicHTTPClient resolves and dials only public Internet addresses. The web
// fetch tool accepts model-generated URLs, so allowing loopback, private, or
// link-local destinations would expose services on the user's machine/network.
func publicHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid destination %q: %w", address, err)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", host, err)
			}
			for _, resolved := range ips {
				if !isPublicIP(resolved.IP) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			}
			return nil, fmt.Errorf("destination %q does not resolve to a public IP address", host)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// HTMLToMarkdown converts an HTML string into structured, readable Markdown.
func HTMLToMarkdown(htmlContent, baseURL string) string {
	doc, err := xhtml.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return stripTagsRegex(htmlContent)
	}

	var sb strings.Builder
	convertNode(&sb, doc)

	res := sb.String()
	res = html.UnescapeString(res)

	// Clean up consecutive empty lines
	reLines := regexp.MustCompile(`\n{3,}`)
	res = reLines.ReplaceAllString(res, "\n\n")

	return strings.TrimSpace(res)
}

func convertNode(sb *strings.Builder, n *xhtml.Node) {
	if n == nil {
		return
	}

	// Skip non-content elements
	if n.Type == xhtml.ElementNode {
		switch strings.ToLower(n.Data) {
		case "script", "style", "noscript", "svg", "nav", "footer", "header", "iframe", "button":
			return
		}
	}

	if n.Type == xhtml.TextNode {
		text := n.Data
		// Normalize whitespace inside text node
		if strings.TrimSpace(text) == "" {
			if strings.Contains(text, "\n") {
				sb.WriteString(" ")
			}
		} else {
			clean := regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
			sb.WriteString(clean)
		}
		return
	}

	tag := strings.ToLower(n.Data)
	switch tag {
	case "h1":
		sb.WriteString("\n\n# ")
	case "h2":
		sb.WriteString("\n\n## ")
	case "h3":
		sb.WriteString("\n\n### ")
	case "h4":
		sb.WriteString("\n\n#### ")
	case "h5", "h6":
		sb.WriteString("\n\n##### ")
	case "p":
		sb.WriteString("\n\n")
	case "br":
		sb.WriteString("\n")
	case "li":
		sb.WriteString("\n* ")
	case "code":
		sb.WriteString("`")
	case "pre":
		sb.WriteString("\n\n```\n")
	case "blockquote":
		sb.WriteString("\n\n> ")
	case "a":
		// Links handled below with href
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		convertNode(sb, c)
	}

	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "blockquote":
		sb.WriteString("\n")
	case "code":
		sb.WriteString("`")
	case "pre":
		sb.WriteString("\n```\n")
	}
}

func extractText(n *xhtml.Node) string {
	var sb strings.Builder
	var walk func(*xhtml.Node)
	walk = func(child *xhtml.Node) {
		if child.Type == xhtml.TextNode {
			sb.WriteString(child.Data)
		}
		for c := child.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return html.UnescapeString(strings.TrimSpace(sb.String()))
}

func stripTagsRegex(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(s, "")
}
