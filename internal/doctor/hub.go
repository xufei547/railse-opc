package doctor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hooksconf"
	"github.com/xufei547/railse-opc/internal/hub/push"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/tailscale"
)

// hubClientChecks 在机器侧看 hub 是否可达、协议是否一致：GET machine.json 里 hub_url 的 /api/version，不带凭据。
func hubClientChecks(o Options) []Check {
	const id, name = "hub.reachable", "hub 可达"
	// 与守护进程用同一个解析与校验；它的错误信息不带文件内容（里面有本机密钥）
	m, err := agent.LoadMachine(filepath.Join(o.AgentDir, "machine.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return []Check{{ID: id, Name: name, Status: Skip, Detail: "没有 machine.json（未接入 hub）"}}
	}
	if err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "railse agent enroll --hub … --token … --force 重新接入"}}
	}
	endpoint := strings.TrimRight(m.HubURL, "/") + "/api/version"
	resp, err := o.HTTP.Get(endpoint)
	if err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认 hub 在跑、地址与端口对、这台机器访问得到它（证书错误时看 hub 的 TLS 配置）"}}
	}
	defer resp.Body.Close()
	var v struct {
		Version    string `json:"version"`
		AgentProto int    `json:"agent_proto"`
	}
	if resp.StatusCode != 200 {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 回 %d", endpoint, resp.StatusCode), Hint: "hub 太旧（没有 /api/version）、地址指向了别的服务，或被重定向（hub_url 应写最终地址）；升级 hub 或 railse agent enroll --force 改地址"}}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: endpoint + " 回的不是 hub 的版本信息", Hint: "确认 hub_url 指向 railse hub，而不是别的服务"}}
	}
	if v.AgentProto != wire.Proto {
		return []Check{{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("hub 协议 %d，本机 %d（hub 版本 %s，本机 %s）", v.AgentProto, wire.Proto, v.Version, o.Version), Hint: "请升级较旧的一方，让 hub 与本机是同一版本"}}
	}
	return []Check{{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf("%s，hub 版本 %s", m.HubURL, v.Version)}}
}

// hubServerChecks 在 hub 机器上看数据目录、数据库、密码、推送密钥、ACME 目录、本机 hub 的对外地址与开机自启。
// 只读打开数据库，不跑迁移。
func hubServerChecks(o Options) []Check {
	// inDir：这一项看的是数据目录里的东西（目录用不了时按同一个原因跳过）；数据目录本身与开机自启不算
	items := []struct {
		id, name string
		inDir    bool
	}{
		{"hubsrv.dir", "hub 数据目录", false}, {"hubsrv.db", "hub 数据库", true}, {"hubsrv.password", "网页密码", true},
		{"hubsrv.vapid", "推送密钥", true}, {"hubsrv.acme", "ACME 证书目录", true}, {"hubsrv.local", "本机 hub 对外地址", true},
		{"hubsrv.autostart", "hub 开机自启", false},
	}
	skipAll := func(detail string) []Check {
		var out []Check
		for _, it := range items {
			out = append(out, Check{ID: it.id, Name: it.name, Status: Skip, Detail: detail})
		}
		return out
	}
	if o.HubDir == "" {
		if o.HubSkip != "" {
			return skipAll(o.HubSkip)
		}
		return skipAll("这台机器没有 hub 数据目录")
	}
	st, err := os.Stat(o.HubDir)
	usable := err == nil && st.IsDir()
	if !usable && !o.HubDirRequired { // 默认位置没有 hub 的数据目录：这台机器不跑 hub
		return skipAll("没有 hub 数据目录 " + o.HubDir)
	}
	var out []Check
	if !usable {
		// 目录用不了：只在 hubsrv.dir 报原因，依赖数据目录的几项按同一个原因跳过；开机自启不依赖它，照常看
		dir, cause := hubDirUnusable(o.HubDir, err)
		out = append(out, dir)
		for _, it := range items {
			if it.inDir {
				out = append(out, Check{ID: it.id, Name: it.name, Status: Skip, Detail: "hub 数据目录 " + o.HubDir + " " + cause})
			}
		}
	} else {
		out = append(out, permCheck("hubsrv.dir", "hub 数据目录", o.HubDir, 0o700, true, "railse hub run 会建好"))
		out = append(out, hubDBChecks(o)...)
		out = append(out, acmeCheck(o), localHubCheck(o))
	}
	out = append(out, autostartCheck("hubsrv.autostart", "hub 开机自启", o, "hub"))
	return out
}

// hubDirUnusable 给出显式给的 hub 数据目录用不了时 hubsrv.dir 的结论与简短原因（给其余几项的跳过说明）。
// statErr 为 nil 表示路径在、但不是目录。路径中间有一段是文件（ENOTDIR）也算不存在。
func hubDirUnusable(dir string, statErr error) (Check, string) {
	const id, name = "hubsrv.dir", "hub 数据目录"
	switch {
	case statErr == nil:
		return Check{ID: id, Name: name, Status: Fail, Detail: dir + " 不是目录", Hint: "--hub-dir 要指向 hub 的数据目录（railse hub run --dir 用的那个）"}, "不是目录"
	case errors.Is(statErr, fs.ErrNotExist) || errors.Is(statErr, syscall.ENOTDIR):
		return Check{ID: id, Name: name, Status: Fail, Detail: dir + " 不存在", Hint: "确认 --hub-dir 写对了（railse hub run --dir 用的那个目录）；这台机器不跑 hub 就去掉 --hub-dir"}, "不存在"
	}
	why := statErr
	var pe *fs.PathError
	if errors.As(statErr, &pe) { // 细节里不重复路径
		why = pe.Err
	}
	return Check{ID: id, Name: name, Status: Fail, Detail: dir + " 读不到：" + why.Error(), Hint: "确认用跑 hub 的同一个用户运行 doctor"}, "读不到"
}

// 只读打开 hub.db 的两种 URI 参数。hub 没在跑（没有 -wal）时用 immutable：SQLite 不加锁、不建 -wal 与 -shm
// （单用 mode=ro 打开 WAL 库会建出 -wal、-shm 并留下，sudo 跑 doctor 时属主还是 root，hub 之后就写不了库）。
// 有 -wal 时（hub 在跑，或异常退出没检查点）用 readonly_shm：只读地用现成的 -shm，读得到还在 WAL 里的数据；
// 它打不开（CANTOPEN，如崩溃后 -shm 被删）而不新建时，只有 -wal 是空的才回退 immutable（这时读到的就是全部数据）；
// -wal 里有数据而 -shm 不在时 immutable 只读得到最后一次检查点（从没检查点的库连表都没有），doctor 只读、不做恢复，
// 报 errWALPending，不按旧数据下结论。
// busy timeout 只对加锁的那种有用：hub 关库、崩溃恢复时会短暂独占，等一会儿而不是立刻报 SQLITE_BUSY。
const (
	dsnImmutable   = "mode=ro&immutable=1"
	dsnReadonlySHM = "mode=ro&readonly_shm=1&_busy_timeout=2000"
)

// errWALPending 表示 -wal 里有没检查点的数据而 -shm 不在：要等 hub 下次启动时恢复，doctor 不读旧数据。
var errWALPending = errors.New("WAL 里有未检查点的数据而 -shm 不在")

// hubDB 是只读打开、读过 schema_version 的 hub.db。
type hubDB struct {
	db      *sql.DB
	version int
}

// openHubDB 只读打开 hub.db 并读出 schema_version，不建任何文件。路径按 URL 编码（同 store.Open），
// 路径里的 %、#、? 不会被当成转义、片段或参数。sql.Open 不碰文件，第一次查询才真正打开，所以回退要看查询的错误。
func openHubDB(path string) (*hubDB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	tries := []string{dsnImmutable}
	if _, err := os.Lstat(abs + "-wal"); err == nil {
		tries = []string{dsnReadonlySHM, dsnImmutable}
	}
	var lastErr error
	for _, query := range tries {
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: query}).String())
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		var v int
		if err = db.QueryRow("SELECT v FROM schema_version").Scan(&v); err == nil {
			return &hubDB{db: db, version: v}, nil
		}
		db.Close()
		lastErr = err
		if sqliteCode(err) != sqlite3.SQLITE_CANTOPEN || query != dsnReadonlySHM {
			break
		}
		if wal, err := os.Lstat(abs + "-wal"); err == nil && wal.Size() > 0 {
			if _, err := os.Lstat(abs + "-shm"); errors.Is(err, fs.ErrNotExist) {
				return nil, errWALPending
			}
			break // -shm 在却打不开（多半是权限）：照实报，不读旧数据
		}
	}
	return nil, lastErr
}

// sqliteCode 取 SQLite 的主结果码（扩展码的低 8 位）；不是 SQLite 的错误时为 0。
func sqliteCode(err error) int {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return e.Code() & 0xff
	}
	return 0
}

// dbErrHint 按错误给修复提示。只有库真坏了（不是数据库、损坏）才叫人从备份恢复或移走它；
// 被锁、打不开、没权限都是暂时的或环境问题，移走库会白白丢掉机器与规则。
func dbErrHint(err error) string {
	switch code := sqliteCode(err); {
	case code == sqlite3.SQLITE_NOTADB || code == sqlite3.SQLITE_CORRUPT:
		return "hub.db 损坏或不是 hub 的数据库：从备份恢复，或移走它让 railse hub run 新建（机器要重新接入）"
	case code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED:
		return "hub 正持着库的锁（关库或崩溃恢复中），稍后重跑 railse doctor"
	case code == sqlite3.SQLITE_CANTOPEN || code == sqlite3.SQLITE_PERM || code == sqlite3.SQLITE_READONLY || code == sqlite3.SQLITE_AUTH:
		return "暂时读不了：确认用跑 hub 的同一个用户运行 doctor，hub.db 与 -wal、-shm 的属主和权限对，稍后重跑 railse doctor"
	case strings.Contains(err.Error(), "no such table"):
		return "库里没有 hub 的表（不是 hub 建的库，或建库中途断了）：railse hub run 会补建；不是 hub 的数据目录就换对 --hub-dir"
	}
	return "暂时读不了，稍后重跑 railse doctor；一直这样就带上这条报错反馈"
}

// hubDBChecks 给出 hubsrv.db、hubsrv.password、hubsrv.vapid 三项。
func hubDBChecks(o Options) []Check {
	const id, name = "hubsrv.db", "hub 数据库"
	dbPath := filepath.Join(o.HubDir, "hub.db")
	skipRest := func(detail string) []Check {
		return []Check{
			{ID: "hubsrv.password", Name: "网页密码", Status: Skip, Detail: detail},
			{ID: "hubsrv.vapid", Name: "推送密钥", Status: Skip, Detail: detail},
		}
	}
	perm := permCheck(id, name, dbPath, 0o600, false, "railse hub run 会建好数据库")
	if _, err := os.Stat(dbPath); err != nil {
		return append([]Check{perm}, skipRest("没有数据库")...)
	}
	h, err := openHubDB(dbPath)
	if errors.Is(err, errWALPending) {
		return append([]Check{withPerm(perm, Check{ID: id, Name: name, Status: Warn, Detail: "hub 上次没正常退出且 -shm 不在，WAL 里有未检查点的数据，doctor 只读不做恢复", Hint: "启动一次 railse hub run（会自动恢复）后重跑 railse doctor"})}, skipRest("WAL 里有未恢复的数据，不按旧数据下结论")...)
	}
	if err != nil {
		return append([]Check{withPerm(perm, Check{ID: id, Name: name, Status: Fail, Detail: "读不到 schema_version：" + err.Error(), Hint: dbErrHint(err)})}, skipRest("数据库读不了")...)
	}
	defer h.db.Close()
	var schema Check
	switch v := h.version; {
	case v > store.SchemaVersion:
		schema = Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("库版本 %d 比程序（%d）新，hub 起不来", v, store.SchemaVersion), Hint: "升级 railse 到建这个库的版本或更新"}
	case v < store.SchemaVersion:
		schema = Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("库版本 %d，程序 %d", v, store.SchemaVersion), Hint: "下次 railse hub run 会自动升级（建议先备份 hub.db）"}
	default:
		schema = Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf("%s，版本 %d", dbPath, v)}
	}
	out := []Check{withPerm(perm, schema)}

	setting := func(key string) (string, bool, error) {
		var val string
		err := h.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return val, err == nil, err
	}
	switch _, ok, err := setting("password_hash"); {
	case err != nil:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Fail, Detail: "读 settings 出错：" + err.Error(), Hint: dbErrHint(err)})
	case ok:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: OK, Detail: "已设置"})
	default:
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Warn, Detail: "还没设密码，网页登录会得到 503", Hint: "railse hub password"})
	}
	// 错误信息不带密钥内容
	switch raw, ok, err := setting("vapid_private"); {
	case err != nil:
		out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Fail, Detail: "读 settings 出错：" + err.Error(), Hint: dbErrHint(err)})
	case !ok:
		out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Warn, Detail: "VAPID 密钥还没生成", Hint: "首次 railse hub run 会生成；也可以 railse hub vapid"})
	default:
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err == nil {
			_, err = push.ParseVAPID(b)
		}
		if err != nil {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Fail, Detail: "VAPID 密钥损坏，hub 起不来", Hint: "railse hub vapid --rotate 重建（所有手机要重新开启通知）"})
		} else {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: OK, Detail: "VAPID 密钥可用"})
		}
	}
	return out
}

// withPerm 把 hub.db 的权限结论并进库的结论：权限没问题就只留库的结论；有问题（permCheck 只会给 fail）时整项 fail，
// 两边的细节与提示都留着。
func withPerm(perm, c Check) Check {
	if perm.Status == OK {
		return c
	}
	c.Status = Fail
	c.Detail = perm.Detail + "；" + c.Detail
	c.Hint = strings.TrimPrefix(c.Hint+"；"+perm.Hint, "；")
	return c
}

// acmeCheck 看 ACME 自动证书目录（账户与证书私钥）：目录 0700，里面的文件 0600。没有这个目录就是没用 ACME。
func acmeCheck(o Options) Check {
	const id, name = "hubsrv.acme", "ACME 证书目录"
	dir := filepath.Join(o.HubDir, "acme")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return Check{ID: id, Name: name, Status: Skip, Detail: "没有 acme 目录（没用 --acme-domain 自动证书）"}
	}
	c := permCheck(id, name, dir, 0o700, true, "railse hub run --acme-domain … 会建好")
	if c.Status != OK {
		return c
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认用跑 hub 的同一个用户运行 doctor"}
	}
	var bad, badPaths []string
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if st.Mode().Perm() != 0o600 {
			bad = append(bad, fmt.Sprintf("%s(%04o)", e.Name(), st.Mode().Perm()))
			badPaths = append(badPaths, p)
		}
	}
	if len(bad) > 0 {
		return Check{ID: id, Name: name, Status: Fail, Detail: "里面有私钥文件不是 0600：" + strings.Join(bad, "、"), Hint: "chmod 600 " + quoteAll(badPaths)}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: dir}
}

// localHubConf 是 setup all --local-hub 写在 hub 数据目录里的 local-hub.json（cmd/railse 的 localHub）：doctor 按同样的字段
// 只读一遍，校验口径同 cmd/railse 的 normalizeLocalHub。
type localHubConf struct {
	Port      int    `json:"port"`       // hub 监听 127.0.0.1:<Port>
	PublicURL string `json:"public_url"` // 手机打开的 https 源
	Provider  string `json:"provider"`   // "tailscale"（Funnel）或 "custom"（自己的隧道）；空当 custom
}

// publicOrigin 按 setup 的口径（cmd/railse 的 parsePublicOrigin）校验对外地址：https、带主机名、不带用户名密码、路径、
// 查询与片段（尾斜杠可有）。返回规范化的源 https://主机[:端口] 与主机名。错误信息只说哪里不合规，不带原文（里面可能有密码）。
func publicOrigin(raw string) (origin, host string, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	switch {
	case perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Hostname() == "":
		return "", "", errors.New("不是带主机名的 https:// 地址")
	case u.User != nil:
		return "", "", errors.New("带了用户名或密码")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", "", errors.New("带了 ? 查询或 # 片段")
	case u.Scheme != "https":
		return "", "", errors.New("不是 https:// 地址")
	case strings.TrimRight(u.EscapedPath(), "/") != "":
		return "", "", errors.New("带了路径，只能是 https://主机[:端口]")
	}
	return "https://" + u.Host, u.Hostname(), nil
}

// localHubCheck 看 setup all --local-hub 装的本机 hub 的对外地址（hubsrv.local）：读 <hub 数据目录>/local-hub.json；
// 由 Tailscale 提供的先看 `tailscale serve status --json` 里 Funnel 是否把 <主机>:443 转到 127.0.0.1:<端口>（sudo 下整项跳过），
// 再（不论哪种）从本机 GET 一次 <对外地址>/api/version。从本机访问不到只 warn：本机代理常拦着 *.ts.net，手机上未必打不开。
// 端口上的 hub 本身由 hub.reachable（按 machine.json 的地址）看，这里不重复。细节里不带文件内容与不合规的地址原文（可能有密码）。
func localHubCheck(o Options) Check {
	const id, name = "hubsrv.local", "本机 hub 对外地址"
	path := filepath.Join(o.HubDir, "local-hub.json")
	// 重跑 setup 的命令带上这个 hub 数据目录（不在默认位置时不带就会另起一个 hub），守护进程不在 ~/.railse 时带 --dir
	rerun := "railse setup all --local-hub --hub-dir " + hooksconf.ShellQuote(o.HubDir) + dirFlag(o)
	custom := false // 文件解得开、对外地址不是 Tailscale 提供的（provider 空当 custom）：重跑时不带 --public-url 会改用 Tailscale
	bad := func(detail string) Check {
		hint := "重跑 " + rerun + " 重写它"
		if custom {
			hint += "（用的是自己的隧道，再带上 --public-url <对外地址>）"
		}
		return Check{ID: id, Name: name, Status: Fail, Detail: detail, Hint: hint}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Check{ID: id, Name: name, Status: Skip, Detail: "没有 local-hub.json（没用 setup all --local-hub 装过本机 hub，或还没装完）"}
	}
	if err != nil {
		why := err
		var pe *fs.PathError
		if errors.As(err, &pe) { // 细节里不重复路径
			why = pe.Err
		}
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 读不到：" + why.Error(), Hint: "确认用跑 hub 的同一个用户运行 doctor"}
	}
	var lh localHubConf
	if json.Unmarshal(data, &lh) != nil { // 解析错误里会带文件内容的片段，不写进细节
		return bad(path + " 不是合法 JSON")
	}
	custom = lh.Provider != "tailscale"
	if lh.Port < 1 || lh.Port > 65535 {
		return bad(fmt.Sprintf("%s 里 port %d 不在 1..65535", path, lh.Port))
	}
	origin, host, err := publicOrigin(lh.PublicURL)
	if err != nil {
		return bad(path + " 里 public_url 不合规：" + err.Error())
	}

	if lh.Provider == "tailscale" {
		if underSudo(o) {
			return Check{ID: id, Name: name, Status: Skip, Detail: "sudo 下不跑 tailscale", Hint: "用平时的用户身份重跑 railse doctor"}
		}
		ts, err := o.TailscalePath()
		if err != nil {
			return Check{ID: id, Name: name, Status: Fail, Detail: "找不到 tailscale（" + origin + " 由 Tailscale Funnel 提供）",
				Hint: "装好并登录 Tailscale（https://tailscale.com/download）后重跑 railse doctor；不再用 Tailscale 就带 --public-url 重跑 " + rerun}
		}
		out, err := o.TailscaleRun(context.Background(), ts, "serve", "status", "--json")
		if err != nil {
			detail := fmt.Sprintf("%s serve status --json 失败：%v", ts, err)
			if first := firstLine(dropWarnings(out), 200); first != "" {
				detail += "；输出：" + first
			}
			return Check{ID: id, Name: name, Status: Fail, Detail: detail, Hint: "确认 Tailscale 在运行且已登录（tailscale status），再重跑 railse doctor"}
		}
		sc, err := tailscale.ParseServeConfig(out)
		if err != nil {
			return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "在终端里跑一次 tailscale serve status --json 看看输出"}
		}
		if !sc.FunnelFor(host, lh.Port) {
			return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("Funnel 没把 %s 转到 127.0.0.1:%d", host, lh.Port),
				Hint: fmt.Sprintf("tailscale funnel --bg %d，或重跑 %s", lh.Port, rerun)}
		}
	}

	endpoint := origin + "/api/version"
	const proxyHint = "从本机访问不到对外地址：可能是本机代理拦截了（见 setup 结尾的提醒），用手机 4G 打开验证；Funnel 入口 TLS 握手失败时先关掉代理再试"
	resp, err := o.HTTP.Get(endpoint)
	if err != nil {
		why := err
		var ue *url.Error
		if errors.As(err, &ue) { // 细节里不重复地址
			why = ue.Err
		}
		return Check{ID: id, Name: name, Status: Warn, Detail: "从本机访问 " + endpoint + " 失败：" + why.Error(), Hint: proxyHint}
	}
	defer resp.Body.Close()
	notHub := fmt.Sprintf("对外地址没转到本机 hub：确认它转到 127.0.0.1:%d（那里的 hub 看 hub.reachable）；也可能是本机代理拦截了，用手机 4G 打开验证", lh.Port)
	if resp.StatusCode != 200 {
		return Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("%s 回 %d", endpoint, resp.StatusCode), Hint: notHub}
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil || v.Version == "" {
		return Check{ID: id, Name: name, Status: Warn, Detail: endpoint + " 回的不是 hub 的版本信息", Hint: notHub}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf("%s，hub 版本 %s", origin, v.Version)}
}

// dropWarnings 去掉 tailscale 输出里以 Warning: 开头的行（如客户端与 tailscaled 版本不一致），摘要取真正的报错。
func dropWarnings(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Warning:") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}
