// Package webui 把网页构建产物（Vite 输出到 dist/）嵌进 hub 二进制，按单页应用的方式托管。
package webui

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FS 返回嵌入的 dist 目录。没构建时只有 .gitkeep。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" 是合法路径，fs.Sub 不会失败
	}
	return sub
}

const (
	// assetsCache 给 assets/ 下的文件：Vite 的产物文件名带内容哈希，内容变了名字就变。
	assetsCache = "public, max-age=31536000, immutable"
	notBuiltMsg = "网页未构建：在仓库里运行 make web 后重新构建 railse\n"
	notFoundAPI = `{"error":"没有这个接口"}` + "\n"
)

// contentTypes 定死几种类型，不随系统的 mime 表变（各系统不同：.js 可能是 application/javascript，.webmanifest 多半没有）。
// Service Worker 脚本必须是 JavaScript 类型，清单是 application/manifest+json。别的扩展名查 mime.TypeByExtension，
// 查不到回 application/octet-stream（响应带 nosniff，浏览器不会再去猜）。
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".json":        "application/json",
	".png":         "image/png",
	".svg":         "image/svg+xml",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := contentTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// Handler 托管 fsys（构建产物）：
//   - /api/、/ws/ 下的路径（hub 的接口路由没认出的才会到这里）回 404 JSON，不论方法，不回 index.html；
//   - 只接受 GET 与 HEAD，其余 405；
//   - 路径是 fsys 里的文件（不含以点开头的）就原样返回：assets/ 下的长期缓存，其余（sw.js、清单、图标）no-cache；
//   - 其余路径（/、/r/abc、目录）回 index.html（no-cache）；没有 index.html（没构建）回 503 提示。
//
// 所有响应带 nosniff、Referrer-Policy 与 X-Frame-Options: DENY；HTML 响应另带 CSP（见 csp）。
func Handler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		p := path.Clean("/" + r.URL.Path)
		if isAPIPath(p) {
			h.Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, notFoundAPI) // 写失败只会是客户端已断开
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "只接受 GET 与 HEAD", http.StatusMethodNotAllowed)
			return
		}
		name, cache := strings.TrimPrefix(p, "/"), "no-cache"
		switch {
		case !isFile(fsys, name):
			name = "index.html"
			if !isFile(fsys, name) {
				h.Set("Cache-Control", "no-store")
				h.Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, notBuiltMsg) // 写失败只会是客户端已断开
				return
			}
		case strings.HasPrefix(name, "assets/"):
			cache = assetsCache
		}
		h.Set("Cache-Control", cache)
		serveFile(w, r, fsys, name)
	})
}

// isAPIPath 报告清理过的路径 p 是否在接口的命名空间里（/api、/ws 及其下）。
func isAPIPath(p string) bool {
	for _, prefix := range []string{"/api", "/ws"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// isFile 报告 name 是 fsys 里的普通文件。空名（根目录）、目录、以点开头的路径段（.gitkeep 之类）都不算。
func isFile(fsys fs.FS, name string) bool {
	if name == "" || !fs.ValidPath(name) {
		return false
	}
	for _, el := range strings.Split(name, "/") {
		if strings.HasPrefix(el, ".") {
			return false
		}
	}
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

// serveFile 用 http.ServeContent 返回文件（它处理 HEAD、Range 与条件请求）。Content-Type 事先按扩展名设好，
// HTML 加 CSP。不用 http.ServeFileFS：它把以 /index.html 结尾的请求路径重定向到目录。
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		serveErr(w)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		serveErr(w)
		return
	}
	rs, ok := f.(io.ReadSeeker) // embed.FS 与 fstest.MapFS 的文件都能 Seek
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			serveErr(w)
			return
		}
		rs = bytes.NewReader(b)
	}
	ct := contentType(name)
	w.Header().Set("Content-Type", ct)
	if strings.HasPrefix(ct, "text/html") {
		w.Header().Set("Content-Security-Policy", csp(r.Host))
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}

func serveErr(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "读网页文件失败", http.StatusInternalServerError)
}

// csp 给出 HTML 响应的内容安全策略：脚本、样式、图片、清单与 Service Worker 只许同源；不许内联脚本、内联样式与 eval
// （Vite 的生产构建没有内联脚本与样式；React 的 style 属性走 CSSOM，不受 style-src 限制）；不许被嵌进框架；
// 不许 <base>、<object>；表单只许提交到本站。
//
// connect-src 在 'self' 之外显式写上本站的 wss:// 与 ws://（host 取请求的 Host）。/ws/client 是同源 WebSocket：CSP3 规定
// 'self' 涵盖同主机的 ws/wss，Chrome、Firefox 与 Safari 16 起（WebKit r292266，bug 235873）照此实现，但 Safari 15 及更早不认，
// 只写 'self' 会挡掉实时通道。Host 只在是“域名或 IPv4，可带端口”时才写进去（见 cspHost）：IPv6 字面量在 CSP 的 host-source
// 里写不出来，带 ;、空格、引号的更不能拼进头里，这些情况只留 'self'。ws:// 只对 http 部署有用；https 页面上浏览器按混合内容
// 规则本来就不许连 ws://。
func csp(host string) string {
	connect := "'self'"
	if h, ok := cspHost(host); ok {
		connect += " wss://" + h + " ws://" + h
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src " + connect +
		"; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// cspHost 把 Host 头规整成小写；只有“由字母、数字、点、连字符组成的主机名（含 IPv4），可带 1 到 5 位数字端口”才返回 true。
func cspHost(host string) (string, bool) {
	host = strings.ToLower(host)
	name, port, hasPort := strings.Cut(host, ":")
	if hasPort && (port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "") {
		return "", false
	}
	if name == "" || len(name) > 253 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
		return "", false
	}
	return host, true
}
