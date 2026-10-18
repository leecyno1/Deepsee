package model

import "testing"

func TestParseSNSContentHandlesCDATAAndMultilineMedia(t *testing.T) {
	raw := `<TimelineObject>
		<username><![CDATA[wxid_friend]]></username>
		<createTime>1712345678</createTime>
		<contentDesc><![CDATA[文本里可以有 <tag> 和 & 字符]]></contentDesc>
		<ContentObject>
			<type>3</type>
			<contentUrl><![CDATA[https://example.test/post?a=1&b=2]]></contentUrl>
			<mediaList>
				<media>
					<type>2</type>
					<title><![CDATA[封面]]></title>
					<thumb><![CDATA[https://example.test/a.jpg?a=1&b=2]]></thumb>
				</media>
			</mediaList>
		</ContentObject>
	</TimelineObject>`

	post, err := ParseSNSContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if post.UserName != "wxid_friend" {
		t.Fatalf("username = %q", post.UserName)
	}
	if post.ContentDesc != "文本里可以有 <tag> 和 & 字符" {
		t.Fatalf("contentDesc = %q", post.ContentDesc)
	}
	if post.Article == nil {
		t.Fatal("multiline article was not parsed")
	}
	if post.Article.URL != "https://example.test/post?a=1&b=2" {
		t.Fatalf("article URL = %q", post.Article.URL)
	}
	if post.Article.CoverURL != "https://example.test/a.jpg?a=1&b=2" {
		t.Fatalf("cover URL = %q", post.Article.CoverURL)
	}
}

func TestParseSNSContentHandlesMultilineFinderFeed(t *testing.T) {
	raw := `<TimelineObject>
		<ContentObject>
			<type>28</type>
			<finderFeed>
				<nickname><![CDATA[视频号作者]]></nickname>
				<desc><![CDATA[多行结构]]></desc>
				<mediaCount>1</mediaCount>
				<media>
					<type>6</type>
					<url><![CDATA[https://example.test/video.mp4]]></url>
					<thumbUrl><![CDATA[https://example.test/thumb.jpg]]></thumbUrl>
				</media>
			</finderFeed>
		</ContentObject>
	</TimelineObject>`
	post, err := ParseSNSContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if post.FinderFeed == nil {
		t.Fatal("multiline finderFeed was not parsed")
	}
	if post.FinderFeed.Nickname != "视频号作者" || post.FinderFeed.VideoURL != "https://example.test/video.mp4" {
		t.Fatalf("finderFeed = %#v", post.FinderFeed)
	}
}

func TestParseSNSContentHandlesObservedNoteAndLivePhotoTypes(t *testing.T) {
	t.Run("type 26 note article", func(t *testing.T) {
		raw := `<TimelineObject><ContentObject><title>收藏笔记</title><description>摘要</description><contentUrl>https://example.test/note</contentUrl><type>26</type><mediaList/></ContentObject></TimelineObject>`
		post, err := ParseSNSContent(raw)
		if err != nil {
			t.Fatal(err)
		}
		if post.ContentType != "article" || post.Article == nil || post.Article.URL != "https://example.test/note" {
			t.Fatalf("post = %#v", post)
		}
	})

	t.Run("type 54 live photo", func(t *testing.T) {
		raw := `<TimelineObject><ContentObject><type>54</type><mediaList><media><type>2</type><url>https://example.test/image.jpg</url><thumb>https://example.test/thumb.jpg</thumb><livePhoto><url>https://example.test/live.mov</url><thumb>https://example.test/live.jpg</thumb></livePhoto></media></mediaList></ContentObject></TimelineObject>`
		post, err := ParseSNSContent(raw)
		if err != nil {
			t.Fatal(err)
		}
		if post.ContentType != "image" || len(post.MediaList) != 1 || post.MediaList[0].LivePhoto == nil {
			t.Fatalf("post = %#v", post)
		}
	})
}

func TestParseSNSContentNormalizesTextAndInteractions(t *testing.T) {
	raw := `<SnsDataItem>
		<TimelineObject>
			<username>wxid_author</username>
			<createTime>1785326400</createTime>
			<contentDesc>我是最厉害的班德尔[胜利][呲牙][加油]</contentDesc>
			<ContentObject><type>1</type></ContentObject>
		</TimelineObject>
		<LocalExtraInfo>
			<like_user_list>
				<user_comment>
					<username>wxid_like</username>
					<nickname>满满</nickname>
					<b_deleted>0</b_deleted>
				</user_comment>
			</like_user_list>
			<comment_user_list>
				<user_comment>
					<comment_64id>123456</comment_64id>
					<username>wxid_author</username>
					<nickname>#</nickname>
					<content>钻石局[发怒]</content>
					<create_time>1785326460</create_time>
					<b_deleted>0</b_deleted>
				</user_comment>
			</comment_user_list>
		</LocalExtraInfo>
	</SnsDataItem>`

	post, err := ParseSNSContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if post.ContentRaw != "我是最厉害的班德尔[胜利][呲牙][加油]" {
		t.Fatalf("raw content = %q", post.ContentRaw)
	}
	if post.ContentDesc != "我是最厉害的班德尔✌️😁💪" {
		t.Fatalf("normalized content = %q", post.ContentDesc)
	}
	if len(post.Likes) != 1 || post.Likes[0].NickName != "满满" {
		t.Fatalf("likes = %#v", post.Likes)
	}
	if len(post.Comments) != 1 {
		t.Fatalf("comments = %#v", post.Comments)
	}
	comment := post.Comments[0]
	if comment.ID != "123456" || comment.Content != "钻石局😡" || comment.ContentRaw != "钻石局[发怒]" {
		t.Fatalf("comment = %#v", comment)
	}
}

func TestParseSNSContentHandlesObservedArticleMetadata(t *testing.T) {
	raw := `<TimelineObject>
		<ContentObject>
			<title>远宠平-守护猫咪的“血压安全线”</title>
			<description>猫咪健康知识</description>
			<contentUrl>https://mp.weixin.qq.com/s/example</contentUrl>
			<sourceNickName>百年远征</sourceNickName>
			<publicUserName>gh_eede8216746f</publicUserName>
			<type>3</type>
			<mediaList>
				<media>
					<thumb token="cover-token" key="510223685" enc_idx="2">https://example.test/150</thumb>
				</media>
			</mediaList>
		</ContentObject>
	</TimelineObject>`

	post, err := ParseSNSContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if post.Article == nil {
		t.Fatal("article was not parsed")
	}
	if post.Article.SourceName != "百年远征" || post.Article.SourceUserName != "gh_eede8216746f" {
		t.Fatalf("article source = %#v", post.Article)
	}
	if post.Article.CoverURL != "https://example.test/150" ||
		post.Article.CoverToken != "cover-token" ||
		post.Article.CoverKey != "510223685" ||
		post.Article.CoverEncIdx != "2" {
		t.Fatalf("article cover = %#v", post.Article)
	}
}

func TestParseSNSContentHandlesObservedUppercaseLivePhoto(t *testing.T) {
	raw := `<TimelineObject>
		<ContentObject>
			<type>54</type>
			<mediaList>
				<media>
					<type>2</type>
					<url>https://example.test/image.jpg</url>
					<thumb>https://example.test/thumb.jpg</thumb>
					<LivePhoto>
						<liveMedia>
							<url>https://example.test/live.mov</url>
							<thumb>https://example.test/live.jpg</thumb>
							<enc key="510223685">1</enc>
						</liveMedia>
					</LivePhoto>
				</media>
			</mediaList>
		</ContentObject>
	</TimelineObject>`

	post, err := ParseSNSContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(post.MediaList) != 1 || post.MediaList[0].LivePhoto == nil {
		t.Fatalf("media = %#v", post.MediaList)
	}
	live := post.MediaList[0].LivePhoto
	if live.URL != "https://example.test/live.mov" || live.Key != "510223685" {
		t.Fatalf("live photo = %#v", live)
	}
}
