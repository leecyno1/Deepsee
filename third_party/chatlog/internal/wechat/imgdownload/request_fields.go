package imgdownload

// CDNFields carries WeChat image CDN metadata from message XML.
// file-id values are not HTTP URLs; they match <img cdnmidimgurl=...> etc.
type CDNFields struct {
	AesKey       string
	CdnMidImgURL string
	CdnBigImgURL string
	CdnThumbURL  string
	HdLength     string
	Length       string
}

// PreferFileID returns the best available CDN file-id for original/HD download.
func (c CDNFields) PreferFileID() string {
	if c.CdnBigImgURL != "" {
		return c.CdnBigImgURL
	}
	if c.CdnMidImgURL != "" {
		return c.CdnMidImgURL
	}
	return c.CdnThumbURL
}

// Usable reports whether enough metadata exists to attempt a native download.
func (c CDNFields) Usable() bool {
	return c.AesKey != "" && c.PreferFileID() != ""
}
