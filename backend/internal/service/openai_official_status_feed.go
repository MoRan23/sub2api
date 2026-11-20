package service

import (
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Official component identities; names alone cannot distinguish the two Login components.
var officialStatusComponentGroups = map[string]int{
	"01JMXBRMFE6N2NNT7DG6XZQ6PW": 0, "01JP8CD9JR3HR6Y7G4Q75N4DVW": 0,
	"01M3Q6W0264T1RJS1YEM47CAQJ": 0, "01M3Q6W0262E8VW5DE1MJMMN0T": 0,
	"01JMXBRMFEMZK0HPK19RYET250": 0, "01JMXBRMFEV0AJ0VVS68N9CD6R": 0,
	"01JMXBRMFE4MAP2BHSJNZ787WX": 0, "01JMXBRMFE5ESNNV8JDHVCGSRD": 0,
	"01JMXBRMFEKVBWKK82B44QFMCE": 0, "01JMXBRMFEVZ7E0X9GD9FWR9WX": 0,
	"01JMXBRMFEQW613TFE89F45035": 0, "01JMXBRMFESJCBGJR10PDD3WCQ": 0,
	"01JSM5RTJWHRWDTS6Q604VEW3B": 0, "01M534EG9N1SYV22NNJPRGYESP": 0,
	"01JMXBNJXGV1T5GT2M9XA83XNG": 1, "01JMXBNJXG1S2D9V65P1ZZTD94": 1,
	"01KX45G1SH21AX5DT93D4HMF0P": 1, "01KMKFAMWKQ81YWSE1Z18R6VHR": 1,
	"01JNKS9D9S72PMP1938PVFFQN4": 1, "01JMXBNJXGKKP51D4DEJ2HZJ8Q": 1,
	"01JMXBNJXG1YMQPPCPCQX3MPA2": 1, "01JMXBNJXGGT5SR5DB9J7GYY48": 1,
	"01JSFK5QX36ZRW0TW0ZV0ZYFXQ": 1, "01JQ7EKW990MSPSWVXC7VPV2ZJ": 1,
	"01JSYVYQSWMJ9QG35XHP08BHA7": 1, "01JSG1XMJ9RVJJQ0E85NVSJ2AZ": 1,
	"01M3Q6NERHVE2WPPAQRHBAC6XB": 1, "01M3Q6NERHS4BG0HQ85VAR1SRH": 1,
	"01KX45G1SHQQ9DTAX9S4W7FV8G": 1, "01K6TVGGGDCP0PPGCHXAG3AQX8": 1,
	"01JVCV8YSWZFRSM1G5CVP253SK": 2, "01KMP3KP5MGE23B80K1EK4S8PV": 2,
	"01KMKFAMWKNQ84Z1766MV08ZDE": 2, "01KMP3KP5M8X0EBTVW6KN327EE": 2,
	"01KKAD7C71MCCH3FTREMJH4AAS": -1, "01KTQBYVARFJ5KMCSECM06VKCF": -1, "01KVR95C58GGWHV7RYBT32NP11": -1,
}

var officialStatusComponentNames = map[string][]string{
	"Chat Completions": {"apis"}, "Responses": {"apis"}, "Agents": {"apis"}, "Decisions": {"apis"},
	"Fine-tuning": {"apis"}, "Embeddings": {"apis"}, "Images": {"apis"}, "Batch": {"apis"},
	"Audio": {"apis"}, "Moderations": {"apis"}, "Realtime": {"apis"}, "Files": {"apis"}, "Live": {"apis"},
	"Conversations": {"chatgpt"}, "ChatGPT Work": {"chatgpt"},
	"Codex in ChatGPT Desktop": {"chatgpt"}, "Compliance API": {"chatgpt"}, "Search": {"chatgpt"},
	"File uploads": {"chatgpt"}, "Voice mode": {"chatgpt"}, "GPTs": {"chatgpt"}, "Image Generation": {"chatgpt"},
	"Deep Research": {"chatgpt"}, "Agent": {"chatgpt"}, "dots": {"chatgpt"}, "Space": {"chatgpt"},
	"Sites": {"chatgpt"}, "Connectors/Apps": {"chatgpt"}, "Codex Web": {"codex"}, "Codex API": {"codex"},
	"CLI": {"codex"}, "VS Code extension": {"codex"}, "FedRAMP": {}, "Ads Manager": {}, "Ads API": {},
}

type officialStatusAtomEntry struct {
	ID      string    `xml:"id"`
	Title   string    `xml:"title"`
	Updated time.Time `xml:"updated"`
	Links   []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	Summary string `xml:"summary"`
	Content string `xml:"content"`
}

func parseOfficialStatusFeed(body []byte) ([]OfficialStatusIncident, error) {
	var feed struct {
		XMLName xml.Name                  `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []officialStatusAtomEntry `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	items := make([]OfficialStatusIncident, 0, min(len(feed.Entries), 100))
	seen := make(map[string]int)
	valid := 0
	for _, entry := range feed.Entries {
		link := officialStatusIncidentURL(entry.ID)
		for _, candidate := range entry.Links {
			if candidate.Rel == "alternate" || candidate.Rel == "" {
				link = officialStatusIncidentURL(candidate.Href)
				break
			}
		}
		if link == "" || entry.Updated.IsZero() {
			continue
		}
		content := entry.Content
		if content == "" {
			content = entry.Summary
		}
		status, components, summary, err := parseOfficialStatusContent(content)
		if err != nil {
			continue
		}
		valid++
		groups := make([]string, 0, 3)
		onlyExcluded := len(components) > 0
		for _, component := range components {
			matched, known := officialStatusComponentNames[component]
			if !known || len(matched) > 0 {
				onlyExcluded = false
			}
			for _, group := range matched {
				if !slices.Contains(groups, group) {
					groups = append(groups, group)
				}
			}
		}
		if onlyExcluded {
			continue
		}
		slices.Sort(groups)
		item := OfficialStatusIncident{ID: path.Base(link), Title: officialStatusText(entry.Title, 500), URL: link,
			Status: status, Components: components, Groups: groups, UpdatedAt: entry.Updated, Summary: officialStatusText(summary, 1000)}
		if index, exists := seen[item.ID]; exists {
			if item.UpdatedAt.After(items[index].UpdatedAt) {
				items[index] = item
			}
			continue
		}
		seen[item.ID] = len(items)
		items = append(items, item)
	}
	if len(feed.Entries) > 0 && valid == 0 {
		return nil, errors.New("invalid official incident entries")
	}
	slices.SortFunc(items, func(a, b OfficialStatusIncident) int {
		if order := b.UpdatedAt.Compare(a.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(items) > 100 {
		items = slices.Clone(items[:100])
	}
	return items, nil
}

func officialStatusIncidentURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "status.openai.com" || u.User != nil {
		return ""
	}
	u.Path = path.Clean(u.Path)
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	if !strings.HasPrefix(u.Path, "/incidents/") || len(u.Path) > 256 || strings.Contains(strings.TrimPrefix(u.Path, "/incidents/"), "/") {
		return ""
	}
	return u.String()
}

func parseOfficialStatusContent(content string) (string, []string, string, error) {
	tokens := html.NewTokenizer(strings.NewReader(content))
	status := "unknown"
	components := make([]string, 0)
	var summary, item strings.Builder
	listDepth, itemDepth := 0, 0
	ignoredTag := ""
	appendComponent := func() {
		name := strings.TrimSpace(item.String())
		if i := strings.LastIndex(name, " ("); i >= 0 {
			name = name[:i]
		}
		name = officialStatusText(name, 150)
		if name != "" && len(components) < 64 && !slices.Contains(components, name) {
			components = append(components, name)
		}
		item.Reset()
	}
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			if tokens.Err() != io.EOF {
				return "", nil, "", tokens.Err()
			}
			if itemDepth > 0 {
				appendComponent()
			}
			return status, components, summary.String(), nil
		case html.StartTagToken:
			tag, _ := tokens.TagName()
			if string(tag) == "script" || string(tag) == "style" {
				ignoredTag = string(tag)
				continue
			}
			if ignoredTag != "" {
				continue
			}
			switch string(tag) {
			case "ul", "ol":
				listDepth++
			case "li":
				itemDepth++
			}
		case html.EndTagToken:
			tag, _ := tokens.TagName()
			if ignoredTag != "" {
				if string(tag) == ignoredTag {
					ignoredTag = ""
				}
				continue
			}
			switch string(tag) {
			case "ul", "ol":
				listDepth = max(0, listDepth-1)
			case "li":
				itemDepth = max(0, itemDepth-1)
				if itemDepth == 0 {
					appendComponent()
				}
			}
		case html.TextToken:
			if ignoredTag != "" {
				continue
			}
			if itemDepth > 0 {
				item.Write(tokens.Text())
				continue
			}
			if listDepth > 0 {
				continue
			}
			text := strings.TrimSpace(string(tokens.Text()))
			if strings.HasPrefix(text, "Status:") {
				candidate := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "Status:")))
				switch candidate {
				case "resolved", "investigating", "identified", "monitoring", "postmortem":
					status = candidate
				}
				continue
			}
			if text != "Affected components" {
				summary.WriteString(text)
				summary.WriteByte(' ')
			}
		}
	}
}

func officialStatusText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	count := 0
	for index := range value {
		if count == limit {
			return strings.Clone(value[:index])
		}
		count++
	}
	return strings.Clone(value)
}
