package handler

import (
	"bytes"
	"net/url"
	"path"

	"golang.org/x/net/html"
)

// filterSimpleHTMLLinks reads hrefs rather than display text. Tokenization
// handles single quotes, entity escaping, and nested link text without
// reserializing the rest of the index.
func (h *PyPIHandler) filterSimpleHTMLLinks(body []byte, versions map[string]bool) []byte {
	if len(versions) == 0 {
		return body
	}
	var result bytes.Buffer
	z := html.NewTokenizer(bytes.NewReader(body))
	skip := false
	for {
		tokenType := z.Next()
		if tokenType == html.ErrorToken {
			return result.Bytes()
		}
		raw := append([]byte(nil), z.Raw()...)
		if tokenType == html.StartTagToken || tokenType == html.SelfClosingTagToken {
			token := z.Token()
			if token.Data == "a" {
				skip = h.deniedSimpleLink(token, versions)
			}
		} else if tokenType == html.EndTagToken && z.Token().Data == "a" {
			if skip {
				skip = false
				continue
			}
		}
		if !skip {
			result.Write(raw)
		}
	}
}

func (h *PyPIHandler) deniedSimpleLink(token html.Token, versions map[string]bool) bool {
	for _, attr := range token.Attr {
		if attr.Key != "href" {
			continue
		}
		u, err := url.Parse(attr.Val)
		if err != nil {
			continue
		}
		_, version := h.parseFilename(path.Base(u.Path))
		if versions[version] {
			return true
		}
	}
	return false
}
