package m3u8

import (
	"bufio"
	"errors"
	"fmt"
	"go-emby2alist/internal/config"
	"go-emby2alist/internal/model"
	"go-emby2alist/internal/service/alist"
	"go-emby2alist/internal/util/color"
	"go-emby2alist/internal/util/https"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NewByContent 根据 m3u8 文本初始化一个 info 对象
//
// 如果文本中的 ts 地址是相对地址, 可通过 baseUrl 指定请求前缀
func NewByContent(baseUrl, content string) (*Info, error) {
	info := Info{RemoteBase: baseUrl}

	// 逐行遍历文本
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineComments := make([]string, 0)
	for scanner.Scan() {
		line := scanner.Text()
		if line = strings.TrimSpace(line); line == "" {
			continue
		}

		// 1 扫描到一行 ts
		if !strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, baseUrl) {
				line = strings.Replace(line, baseUrl, "", 1)
				for strings.HasPrefix(line, "/") {
					line = line[1:]
				}
			}
			tsInfo := TsInfo{Url: line, Comments: lineComments}
			info.RemoteTsInfos = append(info.RemoteTsInfos, &tsInfo)
			lineComments = make([]string, 0)
			continue
		}

		// 2 扫描到注释
		prefix := strings.Split(line, ":")[0]
		if _, ok := ParentHeadComments[prefix]; ok {
			info.HeadComments = append(info.HeadComments, line)
			continue
		}
		if _, ok := ParentTailComments[prefix]; ok {
			info.TailComments = append(info.TailComments, line)
			continue
		}
		lineComments = append(lineComments, line)
	}

	if scanner.Err() != nil {
		return nil, scanner.Err()
	}

	return &info, nil
}

// NewByRemote 从一个远程的 m3u8 地址中初始化 info 对象
func NewByRemote(url string, header http.Header) (*Info, error) {
	// 1 解析 base 地址
	queryPos := strings.Index(url, "?")
	if queryPos == -1 {
		queryPos = len(url)
	}
	lastSepPos := strings.LastIndex(url[:queryPos], "/")
	if lastSepPos == -1 {
		return nil, fmt.Errorf("错误的 m3u8 地址: %s", url)
	}
	baseUrl := url[:lastSepPos+1]

	// 2 请求远程地址
	resp, err := https.Request(http.MethodGet, url, header, nil)
	if err != nil {
		return nil, fmt.Errorf("请求远程地址失败, url: %s, err: %v", url, err)
	}
	defer resp.Body.Close()

	// 3 判断是否为 m3u8 响应
	contentType := resp.Header.Get("Content-Type")
	if _, ok := ValidM3U8Contents[contentType]; !ok {
		return nil, fmt.Errorf("不是有效的 m3u8 响应: %s", contentType)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %v", err)
	}

	// 4 解析远程响应
	return NewByContent(baseUrl, string(bodyBytes))
}

// GetTsLink 获取 ts 流的直链地址
func (i *Info) GetTsLink(idx int) (string, bool) {
	size := len(i.RemoteTsInfos)
	if idx < 0 || idx >= size {
		return "", false
	}
	return i.RemoteBase + i.RemoteTsInfos[idx].Url, true
}

// MasterFunc 获取变体 m3u8
//
// 当 info 包含有字幕时, 需要调用这个方法返回
func (i *Info) MasterFunc(cntMapper func() string) string {
	sb := strings.Builder{}
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	// 写入字幕信息
	for idx, subInfo := range i.Subtitles {
		u, _ := url.Parse("proxy_subtitle")
		q := u.Query()
		q.Set("alist_path", i.AlistPath)
		q.Set("template_id", i.TemplateId)
		q.Set("idx", strconv.Itoa(idx))
		q.Set("api_key", config.C.Emby.ApiKey)
		u.RawQuery = q.Encode()
		cmt := fmt.Sprintf(`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="%s",LANGUAGE="%s",URI="%s"`, subInfo.Lang, subInfo.Lang, u.String())
		sb.WriteString(cmt + "\n")
	}
	sb.WriteString(`#EXT-X-STREAM-INF:SUBTITLES="subs"` + "\n")
	sb.WriteString(cntMapper())
	return sb.String()
}

// ContentFunc 将 i 对象转换成 m3u8 文本
//
// tsMapper 函数可以将当前 info 中的 ts 地址映射为自定义地址
// 两个参数分别是 ts 的索引和地址值
func (i *Info) ContentFunc(tsMapper func(int, string) string) string {
	sb := strings.Builder{}

	// 1 写头注释
	for _, cmt := range i.HeadComments {
		sb.WriteString(cmt + "\n")
	}

	// 2 写 ts
	for idx, ti := range i.RemoteTsInfos {
		for _, cmt := range ti.Comments {
			sb.WriteString(cmt + "\n")
		}
		sb.WriteString(tsMapper(idx, ti.Url) + "\n")
	}

	// 3 写尾注释
	for _, cmt := range i.TailComments {
		sb.WriteString(cmt + "\n")
	}

	res := strings.TrimSuffix(sb.String(), "\n")

	return res
}

// ProxyContent 将 i 转换为 m3u8 本地代理文本
func (i *Info) ProxyContent(main bool) string {
	// 有内封字幕的资源, 切换为变体 m3u8
	if !main && len(i.Subtitles) > 0 {
		return i.MasterFunc(func() string {
			u, _ := url.Parse("proxy_playlist")
			q := u.Query()
			q.Set("alist_path", i.AlistPath)
			q.Set("template_id", i.TemplateId)
			q.Set("api_key", config.C.Emby.ApiKey)
			q.Set("type", "main")
			u.RawQuery = q.Encode()
			return u.String()
		})
	}
	return i.ContentFunc(func(idx int, _ string) string {
		u, _ := url.Parse("proxy_ts")
		q := u.Query()
		q.Set("idx", strconv.Itoa(idx))
		q.Set("alist_path", i.AlistPath)
		q.Set("template_id", i.TemplateId)
		q.Set("api_key", config.C.Emby.ApiKey)
		u.RawQuery = q.Encode()
		return u.String()
	})
}

// Content 将 i 转换为 m3u8 文本
func (i *Info) Content() string {
	return i.ContentFunc(func(_ int, url string) string {
		return i.RemoteBase + url
	})
}

// UpdateContent 从 alist 获取最新的 m3u8 并更新对象
//
// 通过 AlistPath 和 TemplateId 定位到唯一一个转码资源地址
func (i *Info) UpdateContent() error {
	if i.AlistPath == "" || i.TemplateId == "" {
		return errors.New("参数为设置, 无法更新")
	}
	log.Printf(color.ToPurple("更新 playlist, alistPath: %s, templateId: %s"), i.AlistPath, i.TemplateId)

	// 1 如果有 remote 信息, 直接复用
	var newInfo *Info
	var err error
	if i.Remote != "" {
		newInfo, err = NewByRemote(i.Remote, nil)
		if err != nil {
			newInfo = nil
		}
		// remote 信息只使用一次
		i.Remote = ""
	}

	var res model.HttpRes[alist.Resource]
	if newInfo == nil {
		// 2 请求 alist 资源
		res = alist.FetchResource(alist.FetchInfo{
			Path:         i.AlistPath,
			UseTranscode: true,
			Format:       i.TemplateId,
		})
		if res.Code != http.StatusOK {
			return errors.New("请求 alist 失败: " + res.Msg)
		}

		// 3 解析地址
		newInfo, err = NewByRemote(res.Data.Url, nil)
		if err != nil {
			return fmt.Errorf("解析远程 m3u8 失败, url: %s, err: %v", res.Data, err)
		}
	}

	// 4 拷贝最新数据
	i.RemoteBase = newInfo.RemoteBase
	i.HeadComments = append(([]string)(nil), newInfo.HeadComments...)
	i.TailComments = append(([]string)(nil), newInfo.TailComments...)
	i.RemoteTsInfos = append(([]*TsInfo)(nil), newInfo.RemoteTsInfos...)
	i.Subtitles = append(([]alist.SubtitleInfo)(nil), res.Data.Subtitles...)
	i.LastUpdate = time.Now().UnixMilli()
	return nil
}