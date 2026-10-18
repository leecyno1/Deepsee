package http

import (
	"strings"
	"testing"

	"github.com/sjzar/chatlog/internal/model"
)

func TestFavoriteContentPreviewExtractsWeNoteBody(t *testing.T) {
	raw := `<favitem type="18"><datalist count="2">` +
		`<dataitem datatype="8" htmlid="WeNoteHtmlFile"><datatitle>note.htm</datatitle><datadesc>backing file</datadesc></dataitem>` +
		`<dataitem datatype="1" htmlid="-1"><datadesc>readable note body</datadesc></dataitem>` +
		`</datalist></favitem>`
	if got := favoriteContentPreview(raw, 18); got != "readable note body" {
		t.Fatalf("favoriteContentPreview = %q", got)
	}
}

func TestFavoriteContentPreviewPreservesUnknownPayload(t *testing.T) {
	const raw = "plain favorite content"
	if got := favoriteContentPreview(raw, 0); got != raw {
		t.Fatalf("favoriteContentPreview = %q", got)
	}
}

func TestSNSPostSearchContentIncludesParsedMetadata(t *testing.T) {
	post := &model.SNSPost{
		Article: &model.SNSArticle{
			Title:      "血压安全线",
			SourceName: "百年远征",
		},
		Location: &model.SNSLocation{
			City:    "杭州市",
			POIName: "临江商贸城",
		},
		Comments: []model.SNSComment{{
			NickName: "满满",
			Content:  "实现目标",
		}},
	}
	content := snsPostSearchContent(post, "朋友圈正文")
	for _, expected := range []string{"朋友圈正文", "血压安全线", "百年远征", "杭州市", "临江商贸城", "满满", "实现目标"} {
		if !strings.Contains(content, expected) {
			t.Errorf("search content is missing %q: %q", expected, content)
		}
	}
}
