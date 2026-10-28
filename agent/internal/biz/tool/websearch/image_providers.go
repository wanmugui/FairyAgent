package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	htmlstd "html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type imageProvider interface {
	Name() string
	Search(context.Context, string, int) ([]imageSearchHit, error)
}

type bingImageProvider struct {
	client   *http.Client
	endpoint string
}

func newBingImageProvider(client *http.Client) *bingImageProvider {
	return &bingImageProvider{client: client, endpoint: "https://www.bing.com/images/search"}
}

func (p *bingImageProvider) Name() string { return "bing" }

func (p *bingImageProvider) Search(ctx context.Context, query string, n int) ([]imageSearchHit, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("form", "HDRSC2")
	params.Set("first", "1")
	params.Set("adlt", "off")
	body, err := imageSearchHTTPGet(ctx, p.client, p.endpoint+"?"+params.Encode(), "text/html,application/xhtml+xml")
	if err != nil {
		return nil, fmt.Errorf("bing images: %w", err)
	}
	hits := parseBingImagesHTML(string(body), n)
	if len(hits) == 0 {
		return nil, fmt.Errorf("bing images returned no parseable hits (possibly a bot-detection page)")
	}
	return hits, nil
}

type baiduImageProvider struct {
	client   *http.Client
	endpoint string
}

func newBaiduImageProvider(client *http.Client) *baiduImageProvider {
	return &baiduImageProvider{client: client, endpoint: "https://image.baidu.com/search/acjson"}
}

func (p *baiduImageProvider) Name() string { return "baidu" }

func (p *baiduImageProvider) Search(ctx context.Context, query string, n int) ([]imageSearchHit, error) {
	params := url.Values{}
	params.Set("tn", "resultjson_com")
	params.Set("ipn", "rj")
	params.Set("ct", "201326592")
	params.Set("fp", "result")
	params.Set("cl", "2")
	params.Set("lm", "-1")
	params.Set("ie", "utf-8")
	params.Set("oe", "utf-8")
	params.Set("st", "-1")
	params.Set("word", query)
	params.Set("queryWord", query)
	params.Set("face", "0")
	params.Set("istype", "2")
	params.Set("nc", "1")
	params.Set("pn", "0")
	params.Set("rn", fmt.Sprintf("%d", n))
	body, err := imageSearchHTTPGet(ctx, p.client, p.endpoint+"?"+params.Encode(), "application/json,text/plain,*/*")
	if err != nil {
		return nil, fmt.Errorf("baidu images: %w", err)
	}
	hits := parseBaiduImagesJSON(body, query, n)
	if len(hits) == 0 {
		return nil, fmt.Errorf("baidu images returned no parseable hits")
	}
	return hits, nil
}

type baiduImageResponse struct {
	Data []struct {
		ThumbURL         string `json:"thumbURL"`
		MiddleURL        string `json:"middleURL"`
		HoverURL         string `json:"hoverURL"`
		ObjURL           string `json:"objURL"`
		FromPageTitleEnc string `json:"fromPageTitleEnc"`
		FromURLHost      string `json:"fromURLHost"`
		Width            int    `json:"width"`
		Height           int    `json:"height"`
		ReplaceURL       []struct {
			ObjURL  string `json:"ObjURL"`
			ObjURL2 string `json:"ObjURL2"`
		} `json:"replaceUrl"`
	} `json:"data"`
}

func parseBaiduImagesJSON(body []byte, query string, n int) []imageSearchHit {
	var decoded baiduImageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	hits := make([]imageSearchHit, 0, n)
	seen := make(map[string]bool)
	for _, item := range decoded.Data {
		candidates := make([]string, 0, 4)
		for _, replacement := range item.ReplaceURL {
			candidates = append(candidates, replacement.ObjURL, replacement.ObjURL2)
		}
		candidates = append(candidates, item.HoverURL, item.MiddleURL, item.ThumbURL, item.ObjURL)
		imageURL := ""
		for _, candidate := range candidates {
			candidate = CanonicalizeURL(htmlstd.UnescapeString(strings.TrimSpace(candidate)))
			if isHTTPURLValue(candidate) {
				imageURL = candidate
				break
			}
		}
		if imageURL == "" || seen[imageURL] {
			continue
		}
		seen[imageURL] = true
		title := htmlstd.UnescapeString(strings.TrimSpace(item.FromPageTitleEnc))
		if title == "" {
			title = strings.TrimSpace(item.FromURLHost)
		}
		sourceURL := "https://image.baidu.com/search/index?tn=baiduimage&word=" + url.QueryEscape(query)
		hits = append(hits, imageSearchHit{
			Title:        title,
			ImageURL:     imageURL,
			ThumbnailURL: CanonicalizeURL(htmlstd.UnescapeString(item.ThumbURL)),
			SourceURL:    sourceURL,
			Width:        item.Width,
			Height:       item.Height,
		})
		if len(hits) >= n {
			break
		}
	}
	return hits
}

type wikimediaImageProvider struct {
	client   *http.Client
	endpoint string
}

func newWikimediaImageProvider(client *http.Client) *wikimediaImageProvider {
	return &wikimediaImageProvider{client: client, endpoint: "https://commons.wikimedia.org/w/api.php"}
}

func (p *wikimediaImageProvider) Name() string { return "wikimedia" }

func (p *wikimediaImageProvider) Search(ctx context.Context, query string, n int) ([]imageSearchHit, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("generator", "search")
	params.Set("gsrsearch", query)
	params.Set("gsrnamespace", "6")
	params.Set("gsrlimit", fmt.Sprintf("%d", n))
	params.Set("prop", "imageinfo")
	params.Set("iiprop", "url|size|mime")
	params.Set("iiurlwidth", "1600")
	params.Set("format", "json")
	params.Set("origin", "*")
	body, err := imageSearchHTTPGet(ctx, p.client, p.endpoint+"?"+params.Encode(), "application/json")
	if err != nil {
		return nil, fmt.Errorf("wikimedia commons: %w", err)
	}
	hits := parseWikimediaImagesJSON(body, n)
	if len(hits) == 0 {
		return nil, fmt.Errorf("wikimedia commons returned no parseable hits")
	}
	return hits, nil
}

type wikimediaImageResponse struct {
	Query struct {
		Pages map[string]struct {
			Title     string `json:"title"`
			Index     int    `json:"index"`
			ImageInfo []struct {
				URL         string `json:"url"`
				ThumbURL    string `json:"thumburl"`
				Description string `json:"descriptionurl"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
				Mime        string `json:"mime"`
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
}

func parseWikimediaImagesJSON(body []byte, n int) []imageSearchHit {
	var decoded wikimediaImageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	type page struct {
		title string
		index int
		info  struct {
			URL         string
			ThumbURL    string
			Description string
			Width       int
			Height      int
			Mime        string
		}
	}
	pages := make([]page, 0, len(decoded.Query.Pages))
	for _, item := range decoded.Query.Pages {
		if len(item.ImageInfo) == 0 {
			continue
		}
		info := item.ImageInfo[0]
		pages = append(pages, page{title: item.Title, index: item.Index, info: struct {
			URL         string
			ThumbURL    string
			Description string
			Width       int
			Height      int
			Mime        string
		}{URL: info.URL, ThumbURL: info.ThumbURL, Description: info.Description, Width: info.Width, Height: info.Height, Mime: info.Mime}})
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].index == pages[j].index {
			return pages[i].title < pages[j].title
		}
		return pages[i].index < pages[j].index
	})
	hits := make([]imageSearchHit, 0, n)
	for _, item := range pages {
		if !strings.HasPrefix(strings.ToLower(item.info.Mime), "image/") || !isHTTPURLValue(item.info.URL) {
			continue
		}
		hits = append(hits, imageSearchHit{
			Title:        strings.TrimPrefix(item.title, "File:"),
			ImageURL:     item.info.URL,
			ThumbnailURL: item.info.ThumbURL,
			SourceURL:    item.info.Description,
			Width:        item.info.Width,
			Height:       item.info.Height,
		})
		if len(hits) >= n {
			break
		}
	}
	return hits
}

func imageSearchHTTPGet(ctx context.Context, client *http.Client, endpoint, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, imageSearchMaxPageBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}
