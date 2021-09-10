package webseed

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

func trailingPath(infoName string, pathComps []string) string {
	var sb strings.Builder
	sb.WriteString(url.QueryEscape(infoName))
	for i := 0; i < len(pathComps); i += 1 {
		if pathComps[i] == "" {
			continue
		}
		sb.WriteByte('/')
		sb.WriteString(url.QueryEscape(pathComps[i]))
	}
	return sb.String()
}


// Creates a request per BEP 19.
func NewRequest(url_ string, fileIndex int, info *metainfo.Info, offset, length int64) (*http.Request, error) {
	fileInfo := info.UpvertedFiles()[fileIndex]
	if strings.HasSuffix(url_, "/") {
		// BEP specifies that we append the file path. We need to escape each component of the path
		// for things like spaces and '#'.
		url_ += trailingPath(info.Name, fileInfo.Path)
	}
	req, err := http.NewRequest(http.MethodGet, url_, nil)
	if err != nil {
		return nil, err
	}
	if offset != 0 || length != fileInfo.Length {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	}
	return req, nil
}
